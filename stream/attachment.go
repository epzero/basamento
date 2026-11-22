package stream

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/stream/eventlog"
)

// ErrDetached is returned by an Attachment once it is closed.
var ErrDetached = errors.New("stream: attachment closed")

// Frame is one thing a client receives from a run, in order: a durable event, a
// snapshot of an in-flight message, or a delta. Exactly one field is set.
type Frame struct {
	Durable  *eventlog.Event
	Snapshot *Snapshot
	Delta    *Delta
}

// Attachment is one client's stream of a run. It replays the durable events
// after the cursor, then a snapshot of every message still in flight, then the
// live frames until it is closed (REQ-STRM-002, REQ-STRM-003).
type Attachment struct {
	broker *Broker
	run    *liveRun

	mu     sync.Mutex
	buf    []Frame
	notify chan struct{}
	closed bool
	err    error

	seq  int64
	open map[string]int64
}

// Attach joins the run's stream at cursor. The tenant comes from ctx: a run of
// another tenant is not_found and a call without a tenant is invalid_argument
// (REQ-STRM-022, REQ-STRM-025). A run whose live state is gone still replays
// its durable events, so reattaching works across a server restart.
func (b *Broker) Attach(ctx context.Context, runID string, cursor Cursor) (*Attachment, error) {
	run, err := b.ensureRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := checkCursor(cursor); err != nil {
		return nil, err
	}

	// The durable read is the expensive part of an attach (up to
	// maxBufferedFrames rows). It runs without the run's lock: Cancel and
	// Heartbeat must not wait for it past the heartbeat interval
	// (REQ-STRM-005).
	state, err := b.log.State(ctx, runID)
	if err != nil {
		return nil, err
	}
	events, err := b.log.Read(ctx, runID, uint64(cursor.Seq))
	if err != nil {
		return nil, err
	}
	if len(events) > maxBufferedFrames {
		return nil, core.NewError(core.CodeUnavailable,
			"stream: replay is larger than the attachment buffer",
			map[string]any{"events": len(events), "limit": maxBufferedFrames})
	}

	a := &Attachment{
		broker: b,
		run:    run,
		notify: make(chan struct{}, 1),
		seq:    cursor.Seq,
		open:   map[string]int64{},
	}
	for id, dseq := range cursor.Open {
		a.open[id] = dseq
	}

	run.mu.Lock()
	defer run.mu.Unlock()

	// Do not trust only the in-memory flag: another writer may have finished
	// the run in the log since this liveRun was created.
	if terminalState(state) {
		run.markTerminalLocked(state)
	}
	for i := range events {
		a.buf = append(a.buf, Frame{Durable: &events[i]})
		a.seq = int64(events[i].Seq)
	}
	run.noteEvents(events)
	// A durable append may have been delivered live between the read above and
	// this lock: AppendChange records lastSeq under mu before it broadcasts, so
	// an event above the last one replayed was missed. Fetch that tail (usually
	// nothing) instead of leaving a gap (REQ-STRM-002, REQ-STRM-007).
	if run.lastSeq > uint64(a.seq) {
		tail, err := b.log.Read(ctx, runID, uint64(a.seq))
		if err != nil {
			return nil, err
		}
		if len(a.buf)+len(tail) > maxBufferedFrames {
			return nil, core.NewError(core.CodeUnavailable,
				"stream: replay is larger than the attachment buffer",
				map[string]any{"events": len(a.buf) + len(tail), "limit": maxBufferedFrames})
		}
		for i := range tail {
			a.buf = append(a.buf, Frame{Durable: &tail[i]})
			a.seq = int64(tail[i].Seq)
		}
		run.noteEvents(tail)
	}
	// A cursor that claims deltas the live message does not hold would make the
	// snapshot skip the text and leave a gap; reject it instead of replaying
	// silently from a later point. A finished run keeps no in-flight message
	// but can produce no delta either, so its cursor is safe (STRM-002).
	for id, dseq := range a.open {
		msg, ok := run.messages[id]
		if !ok {
			if dseq > 0 && !run.finished {
				return nil, core.NewError(core.CodeInvalidArgument,
					"stream: cursor is ahead of the message",
					map[string]any{"message_id": id, "dseq": dseq, "have": 0})
			}
			continue
		}
		// A completed message needs no snapshot: its durable event carries the
		// text, so a cursor past the live dseq is not a gap. An in-flight one
		// would silently lose the missing deltas.
		if !msg.completed && dseq > msg.dseq {
			return nil, core.NewError(core.CodeInvalidArgument,
				"stream: cursor is ahead of the message",
				map[string]any{"message_id": id, "dseq": dseq, "have": msg.dseq})
		}
	}
	a.buf = append(a.buf, run.snapshotFrames(a.open)...)
	if !run.finished {
		run.subs[a] = struct{}{}
		run.detachedAt = time.Time{}
	}
	return a, nil
}

func checkCursor(cursor Cursor) error {
	if cursor.Seq < 0 {
		return core.NewError(core.CodeInvalidArgument, "stream: negative cursor", nil)
	}
	for id, dseq := range cursor.Open {
		if err := checkMessageID(id); err != nil {
			return err
		}
		if dseq < 0 {
			return core.NewError(core.CodeInvalidArgument, "stream: negative cursor dseq", map[string]any{"message_id": id})
		}
	}
	return nil
}

func (b *Broker) ensureRun(ctx context.Context, runID string) (*liveRun, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	key := runKey(tenant, runID)
	b.mu.Lock()
	run := b.runs[key]
	b.mu.Unlock()
	if run != nil {
		return run, nil
	}
	if _, err := b.log.State(ctx, runID); err != nil {
		return nil, err
	}
	run = newPlaceholder(b.clock, tenant, runID)
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, ok := b.runs[key]; ok {
		return existing, nil
	}
	b.runs[key] = run
	return run, nil
}

// Next returns the next frame, blocking until one is buffered or ctx ends.
func (a *Attachment) Next(ctx context.Context) (Frame, error) {
	for {
		a.mu.Lock()
		if len(a.buf) > 0 {
			frame := a.buf[0]
			a.buf = a.buf[1:]
			a.note(frame)
			a.mu.Unlock()
			return frame, nil
		}
		if a.err != nil {
			err := a.err
			a.mu.Unlock()
			return Frame{}, err
		}
		if a.closed {
			a.mu.Unlock()
			return Frame{}, ErrDetached
		}
		notify := a.notify
		a.mu.Unlock()
		select {
		case <-notify:
		case <-ctx.Done():
			return Frame{}, ctx.Err()
		}
	}
}

// TryNext returns the next buffered frame without blocking. It is what an
// adapter that multiplexes several runs uses between selects.
func (a *Attachment) TryNext() (Frame, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.buf) == 0 {
		return Frame{}, false
	}
	frame := a.buf[0]
	a.buf = a.buf[1:]
	a.note(frame)
	return frame, true
}

// Cursor is the position the client has reached: the last durable sequence and,
// per message, the last delta sequence. It is what the client sends back to
// reattach.
func (a *Attachment) Cursor() Cursor {
	a.mu.Lock()
	defer a.mu.Unlock()
	open := make(map[string]int64, len(a.open))
	for id, dseq := range a.open {
		open[id] = dseq
	}
	return Cursor{Seq: a.seq, Open: open}
}

// Close detaches the client. When it was the last one, the run's detach policy
// starts counting (REQ-STRM-006).
func (a *Attachment) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()

	a.run.mu.Lock()
	if _, ok := a.run.subs[a]; ok {
		delete(a.run.subs, a)
		if len(a.run.subs) == 0 {
			a.run.detachedAt = a.broker.clock.Now()
		}
	}
	a.run.mu.Unlock()
	a.signal()
	return nil
}

// push appends frame unless the attachment is closed. It reports true when
// this call closed the attachment, so the run can remove it from its
// subscribers and start its detach policy (REQ-STRM-006).
func (a *Attachment) push(frame Frame) bool {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	if len(a.buf) >= maxBufferedFrames {
		a.err = core.NewError(core.CodeUnavailable, "stream: attachment fell behind", nil)
		a.closed = true
		a.mu.Unlock()
		a.signal()
		return true
	}
	a.buf = append(a.buf, frame)
	a.mu.Unlock()
	a.signal()
	return false
}

// release ends the attachment from the run's side without an error: once the
// frames already buffered are read, Next returns ErrDetached.
func (a *Attachment) release() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.signal()
}

func (a *Attachment) signal() {
	select {
	case a.notify <- struct{}{}:
	default:
	}
}

func (a *Attachment) note(frame Frame) {
	switch {
	case frame.Durable != nil:
		if int64(frame.Durable.Seq) > a.seq {
			a.seq = int64(frame.Durable.Seq)
		}
	case frame.Snapshot != nil:
		if frame.Snapshot.DSeq > a.open[frame.Snapshot.MessageID] {
			a.open[frame.Snapshot.MessageID] = frame.Snapshot.DSeq
		}
	case frame.Delta != nil:
		if frame.Delta.DSeq > a.open[frame.Delta.MessageID] {
			a.open[frame.Delta.MessageID] = frame.Delta.DSeq
		}
	}
}
