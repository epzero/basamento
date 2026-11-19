package stream

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/stream/eventlog"
)

// DefaultHeartbeat is the longest a producer waits between heartbeats. It is
// the upper bound on how long a cancellation takes to reach an in-flight step
// (REQ-STRM-005).
const DefaultHeartbeat = 5 * time.Second

// maxBufferedFrames bounds one attachment's queue. A client that falls this far
// behind is detached instead of exhausting memory; it reattaches from its
// cursor.
const maxBufferedFrames = 4096

// RunOptions is what a flow's stream declaration asks of the live layer.
type RunOptions struct {
	// DetachAfter is `on_detach: cancel_after(<d>)`; zero keeps the run
	// whatever clients do.
	DetachAfter time.Duration
}

// BrokerOptions configures a Broker.
type BrokerOptions struct {
	// Clock is the only source of time. A nil Clock uses the system clock.
	Clock core.Clock
	// Heartbeat is the producer heartbeat interval; zero means
	// DefaultHeartbeat.
	Heartbeat time.Duration
}

// Broker is the live layer over one durable event log. It fans durable events
// and deltas out to the attachments of every run, keeps the deltas a snapshot
// needs, and enforces the runs' detach policies.
type Broker struct {
	log       *eventlog.Log
	bus       platform.Bus
	clock     core.Clock
	heartbeat time.Duration

	mu   sync.Mutex
	runs map[string]*liveRun
}

type liveRun struct {
	mu sync.Mutex

	tenant      core.TenantID
	id          string
	detachAfter time.Duration
	detachedAt  time.Time

	canceled   chan struct{}
	cancelOnce sync.Once
	finished   bool

	lastSeq  uint64
	messages map[string]*liveMessage
	subs     map[*Attachment]struct{}
}

type liveMessage struct {
	id        string
	dseq      int64
	text      string
	attempt   int
	completed bool
}

// NewBroker builds a broker over log. bus receives the run's deltas; a nil bus
// keeps them in process only, which is what a single-process composition
// needs.
func NewBroker(log *eventlog.Log, bus platform.Bus, opts BrokerOptions) (*Broker, error) {
	if log == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "stream: broker needs an event log", nil)
	}
	clock := opts.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	heartbeat := opts.Heartbeat
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}
	return &Broker{
		log:       log,
		bus:       bus,
		clock:     clock,
		heartbeat: heartbeat,
		runs:      map[string]*liveRun{},
	}, nil
}

// HeartbeatInterval returns the producer heartbeat interval.
func (b *Broker) HeartbeatInterval() time.Duration { return b.heartbeat }

func runKey(tenant core.TenantID, runID string) string { return string(tenant) + "\x00" + runID }

func requireTenant(ctx context.Context) (core.TenantID, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "stream: tenant required", nil)
	}
	return id, nil
}

var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func checkMessageID(messageID string) error {
	if !messageIDPattern.MatchString(messageID) {
		return core.NewError(core.CodeInvalidArgument, "stream: invalid message id", map[string]any{"message_id": messageID})
	}
	return nil
}

// DeltaSubject is the bus subject that retains a run's deltas.
func DeltaSubject(runID string) string { return "run." + runID + ".deltas" }

// Open starts live streaming for a run that the log already holds. The run
// keeps its tenant from ctx. Opening a run twice is a conflict.
func (b *Broker) Open(ctx context.Context, runID string, opts RunOptions) error {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return err
	}
	if opts.DetachAfter < 0 {
		return core.NewError(core.CodeInvalidArgument, "stream: negative detach policy", nil)
	}
	if _, err := b.log.State(ctx, runID); err != nil {
		return err
	}
	run := &liveRun{
		tenant:      tenant,
		id:          runID,
		detachAfter: opts.DetachAfter,
		canceled:    make(chan struct{}),
		messages:    map[string]*liveMessage{},
		subs:        map[*Attachment]struct{}{},
	}
	key := runKey(tenant, runID)
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.runs[key]; ok {
		return core.NewError(core.CodeConflict, "stream: run is already open", map[string]any{"run_id": runID})
	}
	b.runs[key] = run
	return nil
}

func (b *Broker) live(ctx context.Context, runID string) (*liveRun, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	run, ok := b.runs[runKey(tenant, runID)]
	b.mu.Unlock()
	if !ok {
		// The log answers the tenant question with not_found for a run of
		// another tenant; use the same code so the caller learns nothing.
		return nil, core.NewError(core.CodeNotFound, "stream: run is not streaming", map[string]any{"run_id": runID})
	}
	return run, nil
}

// StartMessage records that a message began streaming. A durable
// message.started event is the run's state machine's job; this marks the
// message as in flight so a reattaching client gets its snapshot.
func (b *Broker) StartMessage(ctx context.Context, runID, messageID string) error {
	if err := checkMessageID(messageID); err != nil {
		return err
	}
	run, err := b.live(ctx, runID)
	if err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.writable(); err != nil {
		return err
	}
	run.message(messageID)
	return nil
}

// AppendChange records a state change of the run and the durable events it
// records, then delivers them to every attachment in sequence order. It is the
// only path a durable event takes, so live delivery cannot reorder or duplicate
// what the log numbered (REQ-STRM-002, REQ-STRM-007).
func (b *Broker) AppendChange(ctx context.Context, runID, state string, events []eventlog.NewEvent) ([]eventlog.Event, error) {
	run, err := b.live(ctx, runID)
	if err != nil {
		return nil, err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.writable(); err != nil {
		return nil, err
	}
	out, err := b.log.AppendChange(ctx, runID, state, events)
	if err != nil {
		return nil, err
	}
	run.lastSeq = out[len(out)-1].Seq
	for i := range out {
		run.noteEvent(&out[i])
		run.broadcast(Frame{Durable: &out[i]})
	}
	if terminalState(state) {
		run.finished = true
	}
	return out, nil
}

func terminalState(state string) bool {
	switch state {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// Publish assigns the message's next dseq to a delta, keeps it in the message
// text a snapshot reports, publishes it on the run's delta subject and fans it
// out to every attachment (REQ-STRM-003). It fails with cancelled once the run
// is stopped, so no delta follows a cancellation.
func (b *Broker) Publish(ctx context.Context, runID string, delta Delta) (Delta, error) {
	return b.publish(ctx, runID, delta, false)
}

// Reset starts a new attempt of a message: it clears the partial text, numbers
// the reset after every delta sent so far and sends it before the new attempt's
// deltas (REQ-STRM-004).
func (b *Broker) Reset(ctx context.Context, runID, messageID string, attempt int) (Delta, error) {
	if attempt <= 0 {
		return Delta{}, core.NewError(core.CodeInvalidArgument, "stream: reset needs a positive attempt", nil)
	}
	return b.publish(ctx, runID, Delta{MessageID: messageID, Kind: "reset", Attempt: attempt}, true)
}

func (b *Broker) publish(ctx context.Context, runID string, delta Delta, reset bool) (Delta, error) {
	if err := checkMessageID(delta.MessageID); err != nil {
		return Delta{}, err
	}
	switch delta.Kind {
	case "text", "audio", "progress":
	case "reset":
		if !reset {
			return Delta{}, core.NewError(core.CodeInvalidArgument, "stream: use Reset for a reset delta", nil)
		}
	default:
		return Delta{}, core.NewError(core.CodeInvalidArgument, "stream: unknown delta kind", map[string]any{"kind": delta.Kind})
	}
	run, err := b.live(ctx, runID)
	if err != nil {
		return Delta{}, err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.writable(); err != nil {
		return Delta{}, err
	}
	msg := run.message(delta.MessageID)
	if msg.completed {
		return Delta{}, core.NewError(core.CodeConflict, "stream: message is already completed", map[string]any{"message_id": delta.MessageID})
	}
	msg.dseq++
	delta.RunID = runID
	delta.DSeq = msg.dseq
	if delta.Kind == "reset" {
		msg.text = ""
		msg.attempt = delta.Attempt
	} else {
		if delta.Attempt == 0 {
			delta.Attempt = msg.attempt
		}
		msg.text += delta.Text
	}
	if b.bus != nil {
		data, err := json.Marshal(delta)
		if err != nil {
			return Delta{}, core.NewError(core.CodeInternal, "stream: encode delta", nil)
		}
		if _, err := b.bus.Publish(ctx, DeltaSubject(runID), data); err != nil {
			// The delta was not retained; undo the numbering so a retry keeps
			// the per-message order.
			msg.dseq--
			if delta.Kind != "reset" {
				msg.text = trimSuffix(msg.text, delta.Text)
			}
			return Delta{}, err
		}
	}
	run.broadcast(Frame{Delta: &delta})
	return delta, nil
}

func trimSuffix(text, suffix string) string {
	if suffix == "" || len(text) < len(suffix) {
		return text
	}
	if text[len(text)-len(suffix):] == suffix {
		return text[:len(text)-len(suffix)]
	}
	return text
}

// CompleteMessage records the finished message as a durable event carrying its
// text, so a client that reattaches after it completed still sees the text once
// (REQ-STRM-002). interrupted marks a partial message a cancellation kept.
func (b *Broker) CompleteMessage(ctx context.Context, runID, messageID string, interrupted bool) error {
	if err := checkMessageID(messageID); err != nil {
		return err
	}
	run, err := b.live(ctx, runID)
	if err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.writable(); err != nil {
		return err
	}
	msg, ok := run.messages[messageID]
	if !ok || msg.completed {
		return core.NewError(core.CodeNotFound, "stream: message is not in flight", map[string]any{"message_id": messageID})
	}
	data, err := json.Marshal(map[string]any{
		"message_id":  messageID,
		"text":        msg.text,
		"dseq":        msg.dseq,
		"attempt":     msg.attempt,
		"interrupted": interrupted,
	})
	if err != nil {
		return core.NewError(core.CodeInternal, "stream: encode message.completed", nil)
	}
	events, err := b.log.AppendChange(ctx, runID, run.state(), []eventlog.NewEvent{{Type: "message.completed", Data: data}})
	if err != nil {
		return err
	}
	run.lastSeq = events[len(events)-1].Seq
	msg.completed = true
	for i := range events {
		run.noteEvent(&events[i])
		run.broadcast(Frame{Durable: &events[i]})
	}
	return nil
}

// Cancel stops the run: it closes the cancellation signal the producer reads on
// its next heartbeat, records every in-flight message as interrupted with the
// text received so far, and moves the run to cancelled (REQ-STRM-005).
func (b *Broker) Cancel(ctx context.Context, runID string) error {
	run, err := b.live(ctx, runID)
	if err != nil {
		return err
	}
	return b.cancel(ctx, run)
}

func (b *Broker) cancel(ctx context.Context, run *liveRun) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.finished {
		return nil
	}
	run.cancelOnce.Do(func() { close(run.canceled) })
	ids := make([]string, 0, len(run.messages))
	for id, msg := range run.messages {
		if !msg.completed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	events := make([]eventlog.NewEvent, 0, len(ids)+1)
	for _, id := range ids {
		msg := run.messages[id]
		data, err := json.Marshal(map[string]any{
			"message_id":  id,
			"text":        msg.text,
			"dseq":        msg.dseq,
			"attempt":     msg.attempt,
			"interrupted": true,
		})
		if err != nil {
			return core.NewError(core.CodeInternal, "stream: encode interrupted message", nil)
		}
		events = append(events, eventlog.NewEvent{Type: "message.completed", Data: data})
	}
	events = append(events, eventlog.NewEvent{Type: "run.cancelled"})
	out, err := b.log.AppendChange(ctx, run.id, "cancelled", events)
	if err != nil {
		return err
	}
	run.lastSeq = out[len(out)-1].Seq
	for i := range out {
		run.noteEvent(&out[i])
		run.broadcast(Frame{Durable: &out[i]})
	}
	for _, id := range ids {
		run.messages[id].completed = true
	}
	run.finished = true
	return nil
}

// Heartbeat is the signal a streaming producer sends between deltas. It fails
// with cancelled once the run is stopped, which is how cancellation reaches a
// step within one heartbeat interval (REQ-STRM-005).
func (b *Broker) Heartbeat(ctx context.Context, runID string) error {
	run, err := b.live(ctx, runID)
	if err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.writable()
}

// EnforceDetachPolicies cancels every run whose declared on_detach duration has
// passed with no client attached (REQ-STRM-006). A composition root calls it on
// its clock's tick; it returns the runs it cancelled.
func (b *Broker) EnforceDetachPolicies(_ context.Context) ([]string, error) {
	b.mu.Lock()
	runs := make([]*liveRun, 0, len(b.runs))
	for _, run := range b.runs {
		runs = append(runs, run)
	}
	b.mu.Unlock()
	sort.Slice(runs, func(i, j int) bool { return runs[i].id < runs[j].id })

	now := b.clock.Now()
	var canceled []string
	for _, run := range runs {
		run.mu.Lock()
		expired := run.detachAfter > 0 && !run.finished && len(run.subs) == 0 &&
			!run.detachedAt.IsZero() && !now.Before(run.detachedAt.Add(run.detachAfter))
		run.mu.Unlock()
		if !expired {
			continue
		}
		ctx := core.WithTenant(context.Background(), run.tenant)
		if err := b.cancel(ctx, run); err != nil {
			return canceled, err
		}
		canceled = append(canceled, run.id)
	}
	return canceled, nil
}

// Canceled returns a channel that closes when the run is cancelled. A producer
// selects on it while it streams.
func (b *Broker) Canceled(ctx context.Context, runID string) (<-chan struct{}, error) {
	run, err := b.live(ctx, runID)
	if err != nil {
		return nil, err
	}
	return run.canceled, nil
}

// Finish marks the run closed and drops its retained deltas; a later attach
// replays the durable events, which carry completed messages' text. Both
// operations are safe to repeat.
func (b *Broker) Finish(ctx context.Context, runID string) error {
	run, err := b.live(ctx, runID)
	if err != nil {
		return err
	}
	run.mu.Lock()
	run.finished = true
	run.mu.Unlock()
	if b.bus != nil {
		if err := b.bus.Purge(ctx, DeltaSubject(runID)); err != nil && core.CodeOf(err) != core.CodeNotFound {
			return err
		}
	}
	return nil
}

func (r *liveRun) writable() error {
	select {
	case <-r.canceled:
		return core.NewError(core.CodeCancelled, "stream: run is cancelled", map[string]any{"run_id": r.id})
	default:
	}
	if r.finished {
		return core.NewError(core.CodeConflict, "stream: run has finished", map[string]any{"run_id": r.id})
	}
	return nil
}

func (r *liveRun) state() string {
	if r.finished {
		return "completed"
	}
	return "running"
}

func (r *liveRun) message(id string) *liveMessage {
	msg, ok := r.messages[id]
	if !ok {
		msg = &liveMessage{id: id}
		r.messages[id] = msg
	}
	return msg
}

// noteEvent learns from the durable events the run records: a message.started
// opens a message for snapshots, and a message.completed closes it.
func (r *liveRun) noteEvent(e *eventlog.Event) {
	var data struct {
		MessageID string `json:"message_id"`
	}
	if len(e.Data) > 0 {
		_ = json.Unmarshal(e.Data, &data)
	}
	if data.MessageID == "" {
		return
	}
	msg := r.message(data.MessageID)
	switch e.Type {
	case "message.started":
		msg.completed = false
	case "message.completed":
		msg.completed = true
	}
}

// noteEvents learns from a batch of durable events replayed during an attach.
func (r *liveRun) noteEvents(events []eventlog.Event) {
	for i := range events {
		r.noteEvent(&events[i])
	}
}

func (r *liveRun) broadcast(frame Frame) {
	for a := range r.subs {
		a.push(frame)
	}
}

// snapshotFrames builds one snapshot per message still in flight, in message id
// order, each covering every delta sent so far.
func (r *liveRun) snapshotFrames(seen map[string]int64) []Frame {
	ids := make([]string, 0, len(r.messages))
	for id, msg := range r.messages {
		if !msg.completed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]Frame, 0, len(ids))
	for _, id := range ids {
		msg := r.messages[id]
		if msg.dseq == 0 || msg.dseq < seen[id] {
			continue
		}
		seen[id] = msg.dseq
		out = append(out, Frame{Snapshot: &Snapshot{
			MessageID: id,
			Text:      msg.text,
			DSeq:      msg.dseq,
		}})
	}
	return out
}
