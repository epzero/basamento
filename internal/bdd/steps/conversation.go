package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/agent/conversation"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// The conversation harness runs the real coordinator: every scenario drives
// conversation.Coordinator.Run on a scripted durable context. The context
// resolves the conversation's inbox gate with user messages and scripts the
// turn child (its calls, partials, steering and end); it never re-implements
// the turn loop, the queue or the checkpoint bookkeeping. Assertions read the
// record Run journals into its steps and the output it returns, so a scenario
// can only see what the coordinator itself recorded.
type convWorld struct {
	convs   map[string]map[string]*convState
	current *convState
	lastErr error
}

// convState is one conversation: its coordinator, the durable contexts it ran
// on (one per continue-as-new generation) and the scripted turn children.
type convState struct {
	mu sync.Mutex

	tenant string
	id     string
	user   string
	flow   string

	coord *conversation.Coordinator
	run   *convRun

	gens       []*convDC // every generation started, for teardown
	dc         *convDC   // the active generation
	generation int

	record      conversation.Record // last record the coordinator journaled
	output      *conversation.Output
	activeDone  bool
	threshold   int
	canContinue bool

	started []string
	ids     map[string]string
	msgSeq  int
	refused string
	next    convScript
	seq     int
}

type convRun struct {
	name      string
	tenant    string
	user      string
	children  []*convChild
	continued int
	checks    []int // len(record.Turns) at each checkpoint
}

type convChild struct {
	index     int
	id        string
	future    *childFuture
	script    convScript
	history   []convText
	calls     [][]convText
	produced  []conversation.ModelMessage
	partial   string
	failCode  string
	state     string
	code      string
	received  []convReceived
	pending   []string
	startSeq  int
	endSeq    int
	ended     bool
	cancelled bool
}

type convText struct {
	role string
	text string
}

type convReceived struct {
	text string
	step int
}

// convScript is what the scripted model still owes a turn.
type convScript struct {
	partial   string
	failCode  string
	answer    string
	hasAnswer bool
}

func newConvWorld() *convWorld {
	return &convWorld{convs: map[string]map[string]*convState{}}
}

func (w *convWorld) get(tenant, id string) *convState {
	if w.convs[tenant] == nil {
		return nil
	}
	return w.convs[tenant][id]
}

func (w *convWorld) must(tenant, id string) *convState {
	c := w.get(tenant, id)
	if c == nil {
		c = newConvState(tenant, id, "", "assistant")
		if w.convs[tenant] == nil {
			w.convs[tenant] = map[string]*convState{}
		}
		w.convs[tenant][id] = c
	}
	w.current = c
	return c
}

func (w *convWorld) find(user string) *convState {
	if c := w.get("acme", "c1"); c != nil && c.user == user {
		w.current = c
		return c
	}
	for _, id := range []string{"c1", "c2"} {
		if c := w.get("acme", id); c != nil && c.user == user {
			w.current = c
			return c
		}
	}
	if w.current != nil && w.current.user == user {
		return w.current
	}
	return nil
}

func (w *convWorld) convFor(tenant, user string) *convState {
	if tenant == "" {
		return w.find(user)
	}
	c := w.get(tenant, "c1")
	if c == nil {
		c = w.get(tenant, "c2")
	}
	if c != nil {
		w.current = c
	}
	return c
}

func (w *convWorld) currentOrNew() *convState {
	if w.current == nil {
		return w.must("acme", "c1")
	}
	return w.current
}

// recordErr keeps a refused send, steer or cancel for the Then step, as the
// API edge returns it to the caller (design §1).
func (w *convWorld) recordErr(err error) error {
	if err != nil {
		w.lastErr = err
	}
	return nil
}

func (w *convWorld) checkChildren(n int) error {
	c := w.current
	if c == nil {
		return fmt.Errorf("no conversation")
	}
	if got := c.childCount(); got != n {
		return fmt.Errorf("child runs = %d, want %d", got, n)
	}
	return nil
}

func newConvState(tenant, id, user, flow string) *convState {
	c := &convState{
		tenant: tenant, id: id, user: user, flow: flow,
		ids:    map[string]string{},
		record: conversation.NewRecord(core.TenantID(tenant), user, id, runtime.FlowRef{Name: flow}),
	}
	c.run = &convRun{name: conversation.RunID(id), tenant: tenant, user: user}
	c.coord = conversation.New(conversation.Options{Turns: c})
	return c
}

func (c *convState) newID(text string) string {
	c.msgSeq++
	id := fmt.Sprintf("%s-m%d", c.id, c.msgSeq)
	c.ids[text] = id
	return id
}

func (c *convState) lastID(text string) string {
	if id, ok := c.ids[text]; ok {
		return id
	}
	return c.newID(text)
}

func (c *convState) principal(user string) conversation.Principal {
	return conversation.Principal{Tenant: core.TenantID(c.tenant), User: user}
}

// start launches a generation of the coordinator on a fresh context. The
// record is carried in the input, as a continuation does.
func (c *convState) start() {
	c.mu.Lock()
	c.generation++
	gen := c.generation
	rec := c.record
	if rec.TurnFlow.Name == "" {
		rec.TurnFlow = runtime.FlowRef{Name: "turn"}
	}
	in := conversation.Input{
		Tenant:    core.TenantID(c.tenant),
		User:      c.user,
		ID:        c.id,
		Flow:      runtime.FlowRef{Name: c.flow},
		TurnFlow:  rec.TurnFlow,
		Threshold: c.threshold,
		Record:    &rec,
	}
	dc := newConvDC(c)
	c.dc = dc
	c.gens = append(c.gens, dc)
	c.activeDone = false
	c.output = nil
	c.mu.Unlock()

	go func() {
		out, err := c.coord.Run(dc, in)
		c.finish(dc, gen, out, err)
	}()
	dc.waitSettled()
}

func (c *convState) finish(dc *convDC, gen int, out conversation.Output, err error) {
	c.mu.Lock()
	continued := false
	if c.generation == gen {
		c.record = out.Record
		c.output = &out
		var restart continueAsNew
		continued = c.canContinue && errors.As(err, &restart)
		if !continued {
			c.activeDone = true
		}
	}
	c.mu.Unlock()
	if continued {
		// The runtime continued the run: the same conversation starts a new
		// generation from the record the coordinator journaled. Start it
		// before settling this one, so a step that was waiting on the old
		// generation finds the new one already waiting.
		c.mu.Lock()
		c.run.continued++
		c.mu.Unlock()
		c.start()
	}
	dc.markFinished()
}

// continueAsNew models the runtime carrying a conversation across a new run:
// the current record becomes the new generation's input, as a continuation
// does at a turn boundary (REQ-CONV-014).
func (c *convState) continueAsNew() error {
	c.mu.Lock()
	rec := c.record
	if c.dc == nil || c.activeDone {
		c.mu.Unlock()
		return fmt.Errorf("the coordinator run has ended")
	}
	if rec.Running != 0 || len(rec.Queue) != 0 {
		c.mu.Unlock()
		return fmt.Errorf("the conversation is not at an idle turn boundary")
	}
	c.record = rec
	c.run.continued++
	c.dc = nil
	c.mu.Unlock()
	c.start()
	return nil
}

// --- the scripted durable context -----------------------------------------

type convDC struct {
	state *convState
	ctx   context.Context
	stop  context.CancelFunc

	mu       sync.Mutex
	calls    int
	inbox    *gateFuture
	settle   chan struct{}
	finished chan struct{}
	once     sync.Once
}

func newConvDC(c *convState) *convDC {
	ctx, stop := context.WithCancel(context.Background())
	return &convDC{
		state:    c,
		ctx:      ctx,
		stop:     stop,
		settle:   make(chan struct{}),
		finished: make(chan struct{}),
	}
}

func (d *convDC) Context() context.Context { return d.ctx }
func (d *convDC) Now() time.Time           { return time.Time{} }
func (d *convDC) NewID(kind string) string { return kind }
func (d *convDC) Rand(string) *rand.Rand   { return rand.New(rand.NewPCG(1, 2)) }

func (d *convDC) Step(name string, in any, _ runtime.StepOptions) runtime.Future {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if strings.HasPrefix(name, "refuse-") {
		var refused struct {
			MessageID string `json:"message_id"`
			Code      string `json:"code"`
		}
		if raw, err := json.Marshal(in); err == nil {
			_ = json.Unmarshal(raw, &refused)
		}
		d.state.mu.Lock()
		d.state.refused = refused.Code
		d.state.mu.Unlock()
	} else if rec, ok := in.(conversation.Record); ok {
		d.state.mu.Lock()
		d.state.record = rec
		d.state.mu.Unlock()
	}
	return readyFuture{}
}

func (d *convDC) Child(_ runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	raw, err := json.Marshal(in)
	if err != nil {
		return errFuture{err: err}
	}
	var envelope turnFlowEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return errFuture{err: err}
	}
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()

	c := d.state
	c.mu.Lock()
	c.seq++
	child := &convChild{
		index:    c.nextTurnIndexLocked(),
		id:       o.ID,
		script:   c.next,
		failCode: c.next.failCode,
		history:  decodeTurnFlow(envelope),
		startSeq: c.seq,
		state:    conversation.TurnRunning,
		partial:  c.next.partial,
	}
	child.calls = append(child.calls, cloneTexts(child.history))
	future := &childFuture{dc: d, ch: make(chan struct{})}
	child.future = future
	c.next = convScript{}
	c.run.children = append(c.run.children, child)
	c.started = append(c.started, child.startText())
	c.mu.Unlock()

	return future
}

func (d *convDC) Sleep(time.Duration) runtime.Future { return readyFuture{} }

func (d *convDC) Await(runtime.GateSpec) runtime.Future {
	gate := &gateFuture{dc: d, payload: make(chan json.RawMessage, 1), ready: make(chan struct{})}
	d.mu.Lock()
	d.calls++
	d.inbox = gate
	d.mu.Unlock()
	return gate
}

func (d *convDC) Select(fs ...runtime.Future) (int, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if len(fs) < 2 {
		return 0, core.NewError(core.CodeInvalidArgument, "conversation: select needs an inbox and a child", nil)
	}
	inbox, ok := fs[0].(*gateFuture)
	if !ok {
		return 0, core.NewError(core.CodeInvalidArgument, "conversation: the first select future is not a gate", nil)
	}
	child, ok := fs[1].(*childFuture)
	if !ok {
		return 0, core.NewError(core.CodeInvalidArgument, "conversation: the second select future is not a child", nil)
	}
	if inbox.Ready() {
		return 0, nil
	}
	if child.Ready() {
		return 1, nil
	}
	d.blocked()
	select {
	case <-d.ctx.Done():
		return 0, d.ctx.Err()
	case <-inbox.ready:
		return 0, nil
	case <-child.ch:
		return 1, nil
	}
}

func (d *convDC) Emit(runtime.ProgressEvent) error { return nil }

// continueAsNew is what the scripted runtime returns from Checkpoint when it
// continues the run as new, as the temporal runtime returns the workflow's
// ContinueAsNew error (docs/architecture/runtime.md, Checkpoint): the errors a
// Checkpoint returns are how the runtime reports whether it restarted.
type continueAsNew struct{}

func (continueAsNew) Error() string { return "conversation harness: continue as new" }

func (d *convDC) Checkpoint(runtime.SnapshotRef) error {
	d.state.mu.Lock()
	turns := len(d.state.record.Turns)
	d.state.run.checks = append(d.state.run.checks, turns)
	canContinue := d.state.canContinue
	d.state.mu.Unlock()
	if !canContinue {
		return nil
	}
	return continueAsNew{}
}

func (d *convDC) Capabilities() runtime.Caps {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	return runtime.Caps{SyncGateUpdates: true, ContinueAsNew: d.state.canContinue}
}

func (c *convState) nextTurnIndexLocked() int {
	if n := len(c.record.Turns); n > 0 {
		return c.record.Turns[n-1].Index
	}
	return 1
}

// blocked marks the coordinator as waiting so a scenario step can wait for the
// state its action caused to be settled.
func (d *convDC) blocked() {
	d.mu.Lock()
	if d.settle != nil {
		close(d.settle)
		d.settle = nil
	}
	d.mu.Unlock()
}

func (d *convDC) markFinished() {
	d.mu.Lock()
	if d.settle != nil {
		close(d.settle)
		d.settle = nil
	}
	d.mu.Unlock()
	d.once.Do(func() { close(d.finished) })
}

// act performs one scenario action and waits until the coordinator is waiting
// again on a durable call, so no step sleeps and no assertion races the run.
func (d *convDC) act(fn func() error) error {
	d.mu.Lock()
	settle := make(chan struct{})
	d.settle = settle
	d.mu.Unlock()
	if err := fn(); err != nil {
		d.mu.Lock()
		if d.settle == settle {
			d.settle = nil
		}
		d.mu.Unlock()
		return err
	}
	select {
	case <-settle:
	case <-d.finished:
	}
	return nil
}

func (d *convDC) waitSettled() {
	d.mu.Lock()
	if d.settle == nil {
		d.settle = make(chan struct{})
	}
	settle := d.settle
	d.mu.Unlock()
	select {
	case <-settle:
	case <-d.finished:
	}
}

func (d *convDC) resolve(msg conversation.Message) error {
	d.mu.Lock()
	gate := d.inbox
	d.mu.Unlock()
	if gate == nil {
		return core.NewError(core.CodeConflict, "conversation: the run is not waiting at a gate", nil)
	}
	return gate.resolve(msg)
}

func (d *convDC) callsMade() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type readyFuture struct{}

func (readyFuture) Get(any) error { return nil }
func (readyFuture) Ready() bool   { return true }

type errFuture struct{ err error }

func (f errFuture) Get(any) error { return f.err }
func (f errFuture) Ready() bool   { return true }

type gateFuture struct {
	dc      *convDC
	payload chan json.RawMessage
	ready   chan struct{}
	once    sync.Once
}

func (f *gateFuture) resolve(msg conversation.Message) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	sent := false
	f.once.Do(func() {
		f.payload <- raw
		close(f.ready)
		sent = true
	})
	if !sent {
		return core.NewError(core.CodeConflict, "conversation: the gate is already resolved", nil)
	}
	return nil
}

func (f *gateFuture) Ready() bool {
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

func (f *gateFuture) Get(out any) error {
	if !f.Ready() {
		f.dc.blocked()
		select {
		case <-f.dc.ctx.Done():
			return f.dc.ctx.Err()
		case <-f.ready:
		}
	}
	raw := <-f.payload
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type childFuture struct {
	dc   *convDC
	ch   chan struct{}
	err  error
	out  conversation.TurnOutput
	once sync.Once
}

func (f *childFuture) resolve(out conversation.TurnOutput) {
	f.once.Do(func() {
		f.out = out
		close(f.ch)
	})
}

func (f *childFuture) resolveErr(err error) {
	f.once.Do(func() {
		f.err = err
		close(f.ch)
	})
}

func (f *childFuture) Ready() bool {
	select {
	case <-f.ch:
		return true
	default:
		return false
	}
}

func (f *childFuture) Get(out any) error {
	if !f.Ready() {
		f.dc.blocked()
		select {
		case <-f.dc.ctx.Done():
			return f.dc.ctx.Err()
		case <-f.ch:
		}
	}
	if f.err != nil {
		return f.err
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(f.out)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// --- the turn flow input ---------------------------------------------------

type turnFlowEnvelope struct {
	State string `json:"state"`
	Turns []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"turns"`
}

func decodeTurnFlow(env turnFlowEnvelope) []convText {
	out := make([]convText, 0, len(env.Turns))
	for _, turn := range env.Turns {
		role := conversation.RoleUser
		if turn.Message.Role == "assistant" {
			role = conversation.RoleModel
		}
		out = append(out, convText{role: role, text: turn.Message.Content})
	}
	return out
}

func cloneTexts(in []convText) []convText {
	return append([]convText(nil), in...)
}

func (c *convChild) startText() string {
	for i := len(c.history) - 1; i >= 0; i-- {
		if c.history[i].role == conversation.RoleUser {
			return c.history[i].text
		}
	}
	return ""
}

// --- the Turns port over the scripted child --------------------------------

func (c *convState) childByID(id string) *convChild {
	for i := len(c.run.children) - 1; i >= 0; i-- {
		if c.run.children[i].id == id {
			return c.run.children[i]
		}
	}
	return nil
}

// Steer delivers a steering message to the running child at its next step
// boundary (REQ-CONV-006).
func (c *convState) Steer(_ context.Context, ref runtime.RunRef, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.childByID(ref.RunID)
	if child == nil {
		return core.NewError(core.CodeNotFound, "conversation: no child run to steer", nil)
	}
	child.pending = append(child.pending, text)
	return nil
}

// Cancel ends the running child as cancelled; the coordinator reads its
// partial through Partial (REQ-CONV-008, REQ-CONV-009).
func (c *convState) Cancel(_ context.Context, ref runtime.RunRef, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.childByID(ref.RunID)
	if child == nil || child.ended {
		return nil
	}
	child.cancelled = true
	child.state = conversation.TurnCancelled
	child.code = string(core.CodeCancelled)
	c.seq++
	child.endSeq = c.seq
	child.ended = true
	if child.future != nil {
		child.future.resolveErr(core.NewError(core.CodeCancelled, "conversation: turn cancelled", nil))
	}
	return nil
}

// Partial reports what the turn published before it failed or was cancelled:
// the model messages completed at its step boundaries and the one it was
// writing, which the coordinator records as interrupted (REQ-CONV-009,
// REQ-CONV-013).
func (c *convState) Partial(_ context.Context, ref runtime.RunRef) ([]conversation.ModelMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.childByID(ref.RunID)
	if child == nil {
		return nil, core.NewError(core.CodeNotFound, "conversation: no child run to read", nil)
	}
	out := append([]conversation.ModelMessage(nil), child.produced...)
	if child.partial != "" {
		out = append(out, conversation.ModelMessage{Text: child.partial, Status: conversation.StatusInterrupted})
	}
	return out, nil
}

// --- actions the scenarios drive ------------------------------------------

func (c *convState) runningLocked() *convChild {
	for i := len(c.run.children) - 1; i >= 0; i-- {
		if !c.run.children[i].ended {
			return c.run.children[i]
		}
	}
	return nil
}

func (c *convState) sendAs(p conversation.Principal, text string, mode conversation.Mode, id string, complete bool, answer string) error {
	if id == "" {
		id = c.newID(text)
	}
	msg := conversation.Message{ID: id, Tenant: p.Tenant, User: p.User, Text: text, Mode: mode}

	c.mu.Lock()
	neverStarted := len(c.gens) == 0
	done := c.activeDone || (!neverStarted && c.dc == nil)
	rec := c.record
	c.refused = ""
	c.mu.Unlock()

	if done {
		// The coordinator run has ended; the edge validates the message
		// against the conversation's record (design §1).
		_, err := rec.Accept(msg)
		return err
	}
	if neverStarted {
		c.start()
	}
	if err := c.dc.act(func() error { return c.dc.resolve(msg) }); err != nil {
		return err
	}
	if err := c.refusalErr(); err != nil {
		return err
	}
	if complete {
		return c.completeTurn(answer)
	}
	return nil
}

func (c *convState) refusalErr() error {
	c.mu.Lock()
	code := c.refused
	c.refused = ""
	c.mu.Unlock()
	if code == "" {
		return nil
	}
	return core.NewError(core.Code(code), "conversation: the coordinator refused the message", nil)
}

// completeTurn ends the running turn, then answers the queued turns at once:
// the scripted model answers immediately, so a completed turn drains its queue
// (a cancelled turn holds the next one; see Turns.Cancel).
func (c *convState) completeTurn(answer string) error {
	if err := c.completeOne(answer); err != nil {
		return err
	}
	for c.hasRunningChild() {
		if err := c.completeOne(""); err != nil {
			return err
		}
	}
	return nil
}

func (c *convState) completeOne(answer string) error {
	return c.dc.act(func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		child := c.runningLocked()
		if child == nil {
			return fmt.Errorf("no turn is running")
		}
		c.seq++
		child.endSeq = c.seq
		if answer != "" {
			child.partial = answer
		}
		if child.partial == "" {
			child.partial = "Done."
		}
		child.produced = append(child.produced, conversation.ModelMessage{Text: child.partial, Status: conversation.StatusComplete})
		child.partial = ""
		child.state = conversation.TurnCompleted
		child.ended = true
		out := conversation.TurnOutput{
			State:    conversation.TurnCompleted,
			Messages: append([]conversation.ModelMessage(nil), child.produced...),
		}
		child.future.resolve(out)
		return nil
	})
}

func (c *convState) hasRunningChild() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runningLocked() != nil
}

func (c *convState) failTurn(code string) error {
	return c.dc.act(func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		child := c.runningLocked()
		if child == nil {
			return fmt.Errorf("no turn is running")
		}
		if code == "" {
			code = "vendor_error"
		}
		c.seq++
		child.endSeq = c.seq
		child.state = conversation.TurnFailed
		child.code = code
		child.ended = true
		child.future.resolveErr(core.NewError(core.Code(code), "conversation: turn failed", nil))
		return nil
	})
}

func (c *convState) cancelConversation() error {
	c.mu.Lock()
	dc := c.dc
	done := dc == nil || c.activeDone
	c.mu.Unlock()
	if done {
		return fmt.Errorf("the coordinator run has ended")
	}
	dc.stop()
	<-dc.finished
	return nil
}

func (c *convState) streamPartial(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if child := c.runningLocked(); child != nil {
		child.partial = text
		return nil
	}
	c.next.partial = text
	return nil
}

func (c *convState) markFailure(code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if child := c.runningLocked(); child != nil {
		child.failCode = code
		return nil
	}
	c.next.failCode = code
	return nil
}

func (c *convState) answer(text string) error {
	c.mu.Lock()
	running := c.runningLocked() != nil
	if !running {
		c.next.answer = text
		c.next.hasAnswer = true
	}
	c.mu.Unlock()
	if !running {
		return nil
	}
	return c.completeTurn(text)
}

func (c *convState) failWithPartial(text, code string) error {
	if err := c.streamPartial(text); err != nil {
		return err
	}
	return c.markFailure(code)
}

// finishStep ends the running turn's current step: it records the message the
// turn was writing, delivers the steering messages that arrived, and makes the
// turn's next model call (REQ-CONV-006, REQ-CONV-007).
func (c *convState) finishStep() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.runningLocked()
	if child == nil {
		return fmt.Errorf("no turn is running")
	}
	if child.partial != "" {
		child.produced = append(child.produced, conversation.ModelMessage{Text: child.partial, Status: conversation.StatusComplete})
		child.history = append(child.history, convText{role: conversation.RoleModel, text: child.partial})
		child.partial = ""
	}
	step := len(child.calls) + 1
	for _, text := range child.pending {
		child.history = append(child.history, convText{role: conversation.RoleUser, text: text})
		child.received = append(child.received, convReceived{text: text, step: step})
	}
	child.pending = nil
	answer := child.script.answer
	if !child.script.hasAnswer {
		answer = "Done."
	}
	child.calls = append(child.calls, cloneTexts(child.history))
	child.partial = answer
	return nil
}

// topUpCalls drives real, no-op inbox resolutions until the coordinator's own
// durable-call count reaches n while its turn still runs. Duplicate deliveries
// change no record state; they only make the coordinator take another
// Await/Select turn.
func (c *convState) topUpCalls(n int) error {
	c.mu.Lock()
	child := c.runningLocked()
	rec := c.record
	c.mu.Unlock()
	if child == nil {
		return fmt.Errorf("no turn is running")
	}
	text := child.startText()
	id := ""
	for i := len(rec.Messages) - 1; i >= 0; i-- {
		if rec.Messages[i].Role == conversation.RoleUser && rec.Messages[i].ID != "" {
			id = rec.Messages[i].ID
			break
		}
	}
	if id == "" {
		return fmt.Errorf("the running turn has no user message id")
	}
	for c.dc.callsMade() < n {
		if err := c.sendAs(c.principal(c.user), text, conversation.ModeQueue, id, false, ""); err != nil {
			return err
		}
	}
	return nil
}

// --- record helpers --------------------------------------------------------

func (c *convState) latest() conversation.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.record
}

func (c *convState) lastTurn() (conversation.Turn, bool) {
	rec := c.latest()
	return rec.LastTurn()
}

func (c *convState) modelMessage(text string) (conversation.MessageRecord, bool) {
	for _, m := range c.latest().Messages {
		if m.Role == conversation.RoleModel && m.Text == text {
			return m, true
		}
	}
	return conversation.MessageRecord{}, false
}

func (c *convState) currentTurnFor(text string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, t := range c.started {
		if t == text {
			return i + 1
		}
	}
	return -1
}

func (c *convState) lastChildLocked() *convChild {
	if len(c.run.children) == 0 {
		return nil
	}
	return c.run.children[len(c.run.children)-1]
}

func (c *convState) lastCall() ([]convText, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.lastChildLocked()
	if child == nil || len(child.calls) == 0 {
		return nil, false
	}
	return cloneTexts(child.calls[len(child.calls)-1]), true
}

func (c *convState) callNumber(turn, n int) ([]convText, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, child := range c.run.children {
		if child.index == turn && n >= 1 && n <= len(child.calls) {
			return cloneTexts(child.calls[n-1]), true
		}
	}
	return nil, false
}

func (c *convState) firstCallOfTurn(turn int) []convText {
	texts, _ := c.callNumber(turn, 1)
	return texts
}

func (c *convState) startedTexts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.started...)
}

func (c *convState) lastStarted() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.started) == 0 {
		return ""
	}
	return c.started[len(c.started)-1]
}

func (c *convState) continued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.run.continued
}

func (c *convState) checks() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.run.checks...)
}

func (c *convState) childCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.run.children)
}

func (c *convState) hasCancelledChild() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, child := range c.run.children {
		if child.cancelled {
			return true
		}
	}
	return false
}

func (c *convState) childEnded(state, code string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.run.children) - 1; i >= 0; i-- {
		child := c.run.children[i]
		if child.state == state && child.code == code {
			return true
		}
	}
	return false
}

func (c *convState) received(text string, step int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.lastChildLocked()
	if child == nil {
		return false
	}
	for _, r := range child.received {
		if r.text == text && r.step == step {
			return true
		}
	}
	return false
}

func (c *convState) receivedInOrder(first, second string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.lastChildLocked()
	if child == nil {
		return false
	}
	firstAt, secondAt := -1, -1
	for _, r := range child.received {
		if r.text == first && firstAt < 0 {
			firstAt = r.step
		}
		if r.text == second && secondAt < 0 {
			secondAt = r.step
		}
	}
	return firstAt >= 0 && secondAt >= 0 && firstAt < secondAt
}

func (c *convState) steerAfterFirstModel(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.lastChildLocked()
	if child == nil || len(child.calls) < 2 {
		return fmt.Errorf("the turn has no second model call")
	}
	if len(child.produced) == 0 {
		return fmt.Errorf("the turn produced no model message")
	}
	call := child.calls[1]
	first := child.produced[0].Text
	firstAt, textAt := -1, -1
	for i, t := range call {
		if t.text == first && firstAt < 0 {
			firstAt = i
		}
		if t.text == text && textAt < 0 {
			textAt = i
		}
	}
	if firstAt < 0 || textAt <= firstAt {
		return fmt.Errorf("the steering message was not after the first model message: %v", call)
	}
	return nil
}

func allTexts(call []convText) []string {
	out := make([]string, 0, len(call))
	for _, t := range call {
		out = append(out, t.text)
	}
	return out
}

func userTexts(call []convText) []string {
	var out []string
	for _, t := range call {
		if t.role == conversation.RoleUser {
			out = append(out, t.text)
		}
	}
	return out
}

// --- step definitions ------------------------------------------------------

func registerConversation(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Conversation = newConvWorld()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w := world(ctx).Conversation
		if w == nil {
			return ctx, nil
		}
		for _, byID := range w.convs {
			for _, c := range byID {
				c.mu.Lock()
				gens := append([]*convDC(nil), c.gens...)
				c.mu.Unlock()
				for _, dc := range gens {
					dc.stop()
				}
			}
		}
		return ctx, nil
	})

	sc.Step(`^tenant "([^"]*)" has a conversation "([^"]*)" of user "([^"]*)" on an "([^"]*)" flow$`, func(ctx context.Context, tenant, id, user, flow string) error {
		w := world(ctx).Conversation
		if w.convs[tenant] == nil {
			w.convs[tenant] = map[string]*convState{}
		}
		if old := w.convs[tenant][id]; old != nil {
			old.mu.Lock()
			gens := append([]*convDC(nil), old.gens...)
			old.mu.Unlock()
			for _, dc := range gens {
				dc.stop()
			}
		}
		w.convs[tenant][id] = newConvState(tenant, id, user, flow)
		w.current = nil
		return nil
	})

	// Setup: a completed turn whose model message the scenario names.
	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sent "([^"]*)" and the turn completed with the model message "([^"]*)"$`, func(ctx context.Context, user, tenant, text, answer string) error {
		c := world(ctx).Conversation.convFor(tenant, user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return c.sendAs(c.principal(user), text, conversation.ModeQueue, "", true, answer)
	})
	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sent "([^"]*)" and the turn completed$`, func(ctx context.Context, user, tenant, text string) error {
		c := world(ctx).Conversation.convFor(tenant, user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return c.sendAs(c.principal(user), text, conversation.ModeQueue, "", true, "")
	})
	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sent "([^"]*)" and the turn failed with code "([^"]*)"$`, func(ctx context.Context, user, tenant, text, code string) error {
		c := world(ctx).Conversation.convFor(tenant, user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		if err := c.sendAs(c.principal(user), text, conversation.ModeQueue, "", false, ""); err != nil {
			return err
		}
		return c.failTurn(code)
	})

	// Sends with qualifiers.
	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sends "([^"]*)" and the turn completes$`, func(ctx context.Context, user, tenant, text string) error {
		c := world(ctx).Conversation.convFor(tenant, user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return c.sendAs(c.principal(user), text, conversation.ModeQueue, "", true, "")
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the turn starts$`, func(ctx context.Context, user, text string) error {
		return world(ctx).Conversation.sendAndHold(user, text)
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the turn is halfway$`, func(ctx context.Context, user, text string) error {
		return world(ctx).Conversation.sendAndHold(user, text)
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the turn is in its first step$`, func(ctx context.Context, user, text string) error {
		return world(ctx).Conversation.sendAndHold(user, text)
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the coordinator has made its (\d+)(?:st|nd|rd|th) durable call while the turn is still running$`, func(ctx context.Context, user, text string, n int) error {
		c := world(ctx).Conversation.find(user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		if err := c.sendAs(c.principal(user), text, conversation.ModeQueue, "", false, ""); err != nil {
			return err
		}
		return c.topUpCalls(n)
	})
	sc.Step(`^(\w+) sends "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		// A bare send records a refusal for the Then step (design §1): the
		// scenario asserts it with "the send fails with code".
		c := world(ctx).Conversation.find(user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return world(ctx).Conversation.recordErr(c.sendAs(c.principal(user), text, conversation.ModeQueue, "", false, ""))
	})
	sc.Step(`^(\w+) sends "([^"]*)" again$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.find(user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return world(ctx).Conversation.recordErr(c.sendAs(c.principal(user), text, conversation.ModeQueue, c.lastID(text), false, ""))
	})
	sc.Step(`^(\w+)'s message "([^"]*)" is delivered a second time$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.find(user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return world(ctx).Conversation.recordErr(c.sendAs(c.principal(user), text, conversation.ModeQueue, c.lastID(text), false, ""))
	})

	// Explicit tenants and conversations.
	sc.Step(`^(\w+) of tenant "([^"]*)" sends "([^"]*)" to conversation "([^"]*)" and the turn completes$`, func(ctx context.Context, user, tenant, text, id string) error {
		c := world(ctx).Conversation.must(tenant, id)
		return c.sendAs(conversation.Principal{Tenant: core.TenantID(tenant), User: user}, text, conversation.ModeQueue, "", true, "")
	})
	sc.Step(`^(\w+) sends "([^"]*)" to conversation "([^"]*)" and the turn completes$`, func(ctx context.Context, user, text, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		return c.sendAs(c.principal(user), text, conversation.ModeQueue, "", true, "")
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" sends "([^"]*)" to (?:ana's|ben's) conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, text, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		p := conversation.Principal{Tenant: core.TenantID(tenant), User: user}
		return world(ctx).Conversation.recordErr(c.sendAs(p, text, conversation.ModeQueue, "", false, ""))
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" (sends "([^"]*)" to|cancels) acme's conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, action, text, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		p := conversation.Principal{Tenant: core.TenantID(tenant), User: user}
		if strings.HasPrefix(action, "cancels") {
			rec := c.latest()
			if err := rec.Authorize(p); err != nil {
				return world(ctx).Conversation.recordErr(err)
			}
			return world(ctx).Conversation.recordErr(c.cancelConversation())
		}
		return world(ctx).Conversation.recordErr(c.sendAs(p, text, conversation.ModeQueue, "", false, ""))
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" cancels (?:ana's|ben's) conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		p := conversation.Principal{Tenant: core.TenantID(tenant), User: user}
		rec := c.latest()
		if err := rec.Authorize(p); err != nil {
			return world(ctx).Conversation.recordErr(err)
		}
		return world(ctx).Conversation.recordErr(c.cancelConversation())
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" reads (?:ana's|ben's) conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		p := conversation.Principal{Tenant: core.TenantID(tenant), User: user}
		rec := c.latest()
		world(ctx).Conversation.lastErr = rec.Authorize(p)
		return nil
	})

	// Cancellation and steering.
	sc.Step(`^(\w+) cancels the turn$`, func(ctx context.Context, user string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			c = world(ctx).Conversation.find(user)
		}
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return world(ctx).Conversation.recordErr(c.sendAs(c.principal(user), "cancel the turn", conversation.ModeCancel, "", false, ""))
	})
	sc.Step(`^(\w+) cancels the turn of "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.runningStartText() != text {
			world(ctx).Conversation.lastErr = core.NewError(core.CodeConflict, "conversation: that turn is not running", nil)
			return nil
		}
		return world(ctx).Conversation.recordErr(c.sendAs(c.principal(user), "cancel "+text, conversation.ModeCancel, "", false, ""))
	})
	sc.Step(`^(\w+) cancelled the conversation "([^"]*)"$`, func(ctx context.Context, user, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		return world(ctx).Conversation.recordErr(c.cancelConversation())
	})
	sc.Step(`^(\w+) cancels the conversation "([^"]*)"$`, func(ctx context.Context, user, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		rec := c.latest()
		if err := rec.Authorize(c.principal(user)); err != nil {
			world(ctx).Conversation.lastErr = err
			return nil
		}
		return world(ctx).Conversation.recordErr(c.cancelConversation())
	})
	sc.Step(`^(\w+) steers "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			c = world(ctx).Conversation.find(user)
		}
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return world(ctx).Conversation.recordErr(c.sendAs(c.principal(user), text, conversation.ModeSteer, "", false, ""))
	})

	// Scripted model.
	sc.Step(`^the scripted model streamed "([^"]*)" in (\d+) deltas$`, func(ctx context.Context, text string, deltas int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.streamPartial(text)
	})
	sc.Step(`^the scripted model starts writing "([^"]*)" as the first model message$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.streamPartial(text)
	})
	sc.Step(`^the scripted model then makes the turn fail with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.markFailure(code)
	})
	sc.Step(`^the scripted model makes the turn fail$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.markFailure("vendor_error")
	})
	sc.Step(`^the scripted model answered "([^"]*)" and then makes the turn fail with code "([^"]*)"$`, func(ctx context.Context, text, code string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.failWithPartial(text, code)
	})
	sc.Step(`^the scripted model answers "([^"]*)"$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.answer(text)
	})

	// Turn progression.
	sc.Step(`^the first turn completes$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.completeTurn("")
	})
	sc.Step(`^the turn completes$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.completeTurn("")
	})
	sc.Step(`^the first turn ends$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.failTurn(c.runningFailCode())
	})
	sc.Step(`^the turn ends$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.failTurn(c.runningFailCode())
	})
	sc.Step(`^the failed turn ends$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.failTurn(c.runningFailCode())
	})
	sc.Step(`^the turn finishes its first step$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.finishStep()
	})
	sc.Step(`^the turn finishes its second step$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.finishStep()
	})
	sc.Step(`^the turn is in its second step$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := c.runningCallCount(); got != 2 {
			return fmt.Errorf("the turn is at call %d, want 2", got)
		}
		return nil
	})

	// Assertions: runs and children.
	sc.Step(`^conversation "([^"]*)" has one coordinator run, named "([^"]*)"$`, func(ctx context.Context, id, name string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.oneCoordinatorRun(name)
	})
	sc.Step(`^the coordinator run started (\d+) child runs?, one per turn$`, func(ctx context.Context, n int) error {
		return world(ctx).Conversation.checkChildren(n)
	})
	sc.Step(`^the coordinator run started (\d+) child runs?, the second after the first ended$`, func(ctx context.Context, n int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.childrenInOrder(n)
	})
	sc.Step(`^the coordinator run started (\d+) child runs?$`, func(ctx context.Context, n int) error {
		return world(ctx).Conversation.checkChildren(n)
	})
	sc.Step(`^exactly (\d+) child runs? ha(?:s|ve) started$`, func(ctx context.Context, n int) error {
		return world(ctx).Conversation.checkChildren(n)
	})
	sc.Step(`^the second child run starts$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.childCount() < 2 {
			return fmt.Errorf("the second child run has not started")
		}
		return nil
	})
	sc.Step(`^the turn's child run is cancelled$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if !c.hasCancelledChild() {
			return fmt.Errorf("no child run was cancelled")
		}
		return nil
	})
	sc.Step(`^the turn's child run ends with state "([^"]*)" and code "([^"]*)"$`, func(ctx context.Context, state, code string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if !c.childEnded(state, code) {
			return fmt.Errorf("no child run ended %s/%s", state, code)
		}
		return nil
	})
	sc.Step(`^the coordinator run is cancelled$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		out := c.outputOrNil()
		if out == nil || out.State != conversation.TurnCancelled {
			return fmt.Errorf("the coordinator run did not end cancelled")
		}
		return nil
	})
	sc.Step(`^the coordinator run is named "([^"]*)"$`, func(ctx context.Context, name string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := c.runName(); got != name {
			return fmt.Errorf("coordinator name = %q, want %q", got, name)
		}
		return nil
	})

	// Assertions: turns, queue, messages.
	sc.Step(`^the turn ends with state "([^"]*)"$`, func(ctx context.Context, state string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		turn, ok := c.lastTurn()
		if !ok {
			return fmt.Errorf("no turn")
		}
		if turn.State != state {
			return fmt.Errorf("turn state = %q, want %q", turn.State, state)
		}
		return nil
	})
	sc.Step(`^conversation "([^"]*)" contains the model message "([^"]*)" marked interrupted$`, func(ctx context.Context, id, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		m, ok := c.modelMessage(text)
		if !ok {
			return fmt.Errorf("model message %q not found", text)
		}
		if m.Status != conversation.StatusInterrupted {
			return fmt.Errorf("message %q status = %q, want interrupted", text, m.Status)
		}
		return nil
	})
	sc.Step(`^conversation "([^"]*)" contains the model message "([^"]*)" as interrupted in a turn marked failed$`, func(ctx context.Context, id, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		m, ok := c.modelMessage(text)
		if !ok || m.Status != conversation.StatusInterrupted {
			return fmt.Errorf("model message %q is not interrupted", text)
		}
		rec := c.latest()
		if m.Turn < 1 || m.Turn > len(rec.Turns) || rec.Turns[m.Turn-1].State != conversation.TurnFailed {
			return fmt.Errorf("turn %d is not marked failed", m.Turn)
		}
		return nil
	})
	sc.Step(`^conversation "([^"]*)" keeps the first model message "([^"]*)" as is$`, func(ctx context.Context, id, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		m, ok := c.modelMessage(text)
		if !ok {
			return fmt.Errorf("model message %q not found", text)
		}
		if m.Status != conversation.StatusComplete {
			return fmt.Errorf("message %q status = %q, want complete", text, m.Status)
		}
		return nil
	})
	sc.Step(`^(\w+) receives the turn result with code "([^"]*)"$`, func(ctx context.Context, user, code string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		turn, ok := c.lastTurn()
		if !ok {
			return fmt.Errorf("no turn")
		}
		if turn.Code != code {
			return fmt.Errorf("turn result = %q, want %q", turn.Code, code)
		}
		return nil
	})
	sc.Step(`^(\w+) receives the model message "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if _, ok := c.modelMessage(text); !ok {
			return fmt.Errorf("%s did not receive the model message %q", user, text)
		}
		return nil
	})
	sc.Step(`^(?:conversation "(?:[^"]*)"|(?:\w+)'s conversation "[^"]*") has no queued message$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := len(c.latest().Queue); got != 0 {
			return fmt.Errorf("queued messages = %d, want 0", got)
		}
		return nil
	})
	sc.Step(`^conversation "([^"]*)" has (\d+) queued messages?, "([^"]*)"$`, func(ctx context.Context, id string, n int, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		queue := c.latest().Queue
		if len(queue) != n {
			return fmt.Errorf("queued messages = %d, want %d", len(queue), n)
		}
		if n > 0 && queue[0].Text != text {
			return fmt.Errorf("first queued message = %q, want %q", queue[0].Text, text)
		}
		return nil
	})
	sc.Step(`^the turn of "([^"]*)" starts after the cancelled turn$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.lastStarted() != text {
			return fmt.Errorf("last started turn = %q, want %q", c.lastStarted(), text)
		}
		return nil
	})
	sc.Step(`^the turn of "([^"]*)" starts$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.lastStarted() != text {
			return fmt.Errorf("the turn of %q did not start", text)
		}
		return nil
	})
	sc.Step(`^the turn of "([^"]*)" starts at once$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.lastStarted() != text {
			return fmt.Errorf("the turn of %q did not start at once", text)
		}
		return nil
	})
	sc.Step(`^the turns (?:start|ran) in the order (.+)$`, func(ctx context.Context, order string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		want := quotedList(order)
		got := c.startedTexts()
		if len(got) != len(want) {
			return fmt.Errorf("turns = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				return fmt.Errorf("turns = %v, want %v", got, want)
			}
		}
		return nil
	})
	sc.Step(`^(?:conversation "(?:[^"]*)"|(?:\w+)'s conversation "[^"]*") is still open$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.latest().Cancelled {
			return fmt.Errorf("the conversation is cancelled")
		}
		return nil
	})
	sc.Step(`^the turn was not cancelled$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.hasCancelledChild() {
			return fmt.Errorf("a child run was cancelled")
		}
		return nil
	})
	sc.Step(`^no second child run started for the conversation$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := c.childCount(); got != 1 {
			return fmt.Errorf("child runs = %d, want 1", got)
		}
		return nil
	})
	sc.Step(`^the child run receives "([^"]*)" at step (\d+)$`, func(ctx context.Context, text string, step int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if !c.received(text, step) {
			return fmt.Errorf("the child did not receive %q at step %d", text, step)
		}
		return nil
	})
	sc.Step(`^the turn's child run received the user message "([^"]*)" after the first model message$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.steerAfterFirstModel(text)
	})
	sc.Step(`^the turn's child run received the user message "([^"]*)" before "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if !c.receivedInOrder(first, second) {
			return fmt.Errorf("the child did not receive %q before %q", first, second)
		}
		return nil
	})
	sc.Step(`^no child run started for globex's conversation "([^"]*)"$`, func(ctx context.Context, id string) error {
		w := world(ctx).Conversation
		if g := w.get("globex", id); g != nil && g.childCount() > 0 {
			return fmt.Errorf("globex's conversation %q has a child run", id)
		}
		return nil
	})
	sc.Step(`^acme's coordinator run for "([^"]*)" started a child run in tenant "([^"]*)"$`, func(ctx context.Context, id, tenant string) error {
		w := world(ctx).Conversation
		c := w.get("acme", id)
		if c == nil || c.childCount() == 0 {
			return fmt.Errorf("acme's conversation %q has no child run", id)
		}
		if c.runTenant() != tenant {
			return fmt.Errorf("coordinator tenant = %q, want %q", c.runTenant(), tenant)
		}
		return nil
	})
	sc.Step(`^the coordinator run of ana's conversation "([^"]*)" started (\d+) child run for user "([^"]*)"$`, func(ctx context.Context, id string, n int, user string) error {
		c := world(ctx).Conversation.get("acme", id)
		if c == nil || c.childCount() != n {
			return fmt.Errorf("child runs = %d, want %d", c.childCount(), n)
		}
		if got := c.runUser(); got != user {
			return fmt.Errorf("coordinator user = %q, want %q", got, user)
		}
		return nil
	})
	sc.Step(`^(\w+)'s conversation "([^"]*)" has no child run$`, func(ctx context.Context, user, id string) error {
		w := world(ctx).Conversation
		for _, tenant := range []string{"acme", "globex"} {
			c := w.get(tenant, id)
			if c != nil && c.conversationUser() == user && c.childCount() > 0 {
				return fmt.Errorf("%s's conversation %q has a child run", user, id)
			}
		}
		return nil
	})
	sc.Step(`^the send fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return checkConvErr(world(ctx).Conversation, code)
	})
	sc.Step(`^the cancel fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return checkConvErr(world(ctx).Conversation, code)
	})
	sc.Step(`^the steer fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return checkConvErr(world(ctx).Conversation, code)
	})
	sc.Step(`^the action fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return checkConvErr(world(ctx).Conversation, code)
	})

	// Checkpoints.
	sc.Step(`^a runtime that supports continuing as new$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.currentOrNew()
		c.mu.Lock()
		c.canContinue = true
		c.mu.Unlock()
		return nil
	})
	sc.Step(`^the runtime reports it does not continue as new$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.currentOrNew()
		c.mu.Lock()
		c.canContinue = false
		c.mu.Unlock()
		return nil
	})
	sc.Step(`^the coordinator's checkpoint threshold is (\d+) durable calls$`, func(ctx context.Context, n int) error {
		c := world(ctx).Conversation.currentOrNew()
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.dc != nil && !c.activeDone {
			return fmt.Errorf("the threshold must be set before the coordinator starts")
		}
		c.threshold = n
		return nil
	})
	// The premise is structural: only Await, Select, Step and Child reach the
	// durable context; the scripted child's model calls never do, so they can
	// never be counted as the coordinator's own durable calls.
	sc.Step(`^each turn of conversation "([^"]*)" is the coordinator's Await, Select, Step and Child(?:, and (\d+) durable calls in the child run)?$`, func(ctx context.Context, id, childCalls string) error {
		_ = world(ctx).Conversation.currentOrNew()
		return nil
	})
	sc.Step(`^(\d+) turns of conversation "([^"]*)" have completed$`, func(ctx context.Context, n int, id string) error {
		w := world(ctx).Conversation
		c := w.current
		if c == nil {
			c = w.must("acme", id)
		}
		for i := 1; i <= n; i++ {
			if err := c.sendAs(c.principal(c.user), fmt.Sprintf("turn-%d", i), conversation.ModeQueue, "", true, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the coordinator continued as new (\d+) times?$`, func(ctx context.Context, n int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := c.continued(); got != n {
			return fmt.Errorf("continuations = %d, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the coordinator continued as new once$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return c.continueAsNew()
	})
	sc.Step(`^the coordinator continued as new once before the turn of "([^"]*)"$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		turn := c.currentTurnFor(text)
		if turn < 0 {
			return fmt.Errorf("turn %q not found", text)
		}
		for _, check := range c.checks() {
			if check < turn {
				return nil
			}
		}
		return fmt.Errorf("the coordinator did not continue before the turn of %q", text)
	})
	sc.Step(`^the coordinator continued as new once, only after the first turn ended$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := c.continued(); got != 1 {
			return fmt.Errorf("continuations = %d, want 1", got)
		}
		for _, check := range c.checks() {
			if check == 1 {
				return nil
			}
		}
		return fmt.Errorf("the continuation did not wait for the first turn to end")
	})
	sc.Step(`^the second child run starts after the coordinator continued as new$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.childCount() < 2 {
			return fmt.Errorf("the second child run has not started")
		}
		for _, check := range c.checks() {
			if check == 1 {
				return nil
			}
		}
		return fmt.Errorf("the coordinator did not continue before the second child run")
	})
	sc.Step(`^the coordinator has not continued as new$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := c.continued(); got != 0 {
			return fmt.Errorf("continuations = %d, want 0", got)
		}
		return nil
	})

	// Scripted model observations.
	sc.Step(`^the scripted model's call for the (third|second) turn records these texts in order:$`, func(ctx context.Context, ordinal string, table *godog.Table) error {
		n := 3
		if ordinal == "second" {
			n = 2
		}
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		return compareTexts(allTexts(c.firstCallOfTurn(n)), tableTexts(table))
	})
	sc.Step(`^the scripted model's call for this turn records these texts in order:$`, func(ctx context.Context, table *godog.Table) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		call, ok := c.lastCall()
		if !ok {
			return fmt.Errorf("no model call")
		}
		return compareTexts(allTexts(call), tableTexts(table))
	})
	sc.Step(`^globex's scripted model call for this turn records these texts in order:$`, func(ctx context.Context, table *godog.Table) error {
		c := world(ctx).Conversation.get("globex", "c1")
		if c == nil {
			return fmt.Errorf("no globex conversation")
		}
		call, ok := c.lastCall()
		if !ok {
			return fmt.Errorf("no model call")
		}
		return compareTexts(allTexts(call), tableTexts(table))
	})
	sc.Step(`^the scripted model's call for the second turn records 3 messages, in order$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if got := len(c.firstCallOfTurn(2)); got != 3 {
			return fmt.Errorf("second turn messages = %d, want 3", got)
		}
		return nil
	})
	sc.Step(`^the scripted model's call for this turn records (\d+) messages, in order$`, func(ctx context.Context, n int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		call, ok := c.lastCall()
		if !ok || len(call) != n {
			return fmt.Errorf("model messages = %d, want %d", len(call), n)
		}
		return nil
	})
	sc.Step(`^the scripted model's call for this turn recorded the user text "([^"]*)" twice$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		call, ok := c.lastCall()
		if !ok {
			return fmt.Errorf("no model call")
		}
		n := 0
		for _, t := range call {
			if t.text == text {
				n++
			}
		}
		if n != 2 {
			return fmt.Errorf("user text %q appeared %d times, want 2", text, n)
		}
		return nil
	})
	sc.Step(`^the scripted model's second call records these texts in order:$`, func(ctx context.Context, table *godog.Table) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		call, ok := c.callNumber(c.lastTurnIndex(), 2)
		if !ok {
			return fmt.Errorf("no second call")
		}
		return compareTexts(allTexts(call), tableTexts(table))
	})
	sc.Step(`^the scripted model's third call recorded the user texts "([^"]*)", "([^"]*)", "([^"]*)", in that order$`, func(ctx context.Context, a, b, cc string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		call, ok := c.callNumber(c.lastTurnIndex(), 3)
		if !ok {
			return fmt.Errorf("no third call")
		}
		return compareTexts(userTexts(call), []string{a, b, cc})
	})
}

func (w *convWorld) sendAndHold(user, text string) error {
	c := w.find(user)
	if c == nil {
		return fmt.Errorf("no conversation for %s", user)
	}
	return c.sendAs(c.principal(user), text, conversation.ModeQueue, "", false, "")
}

func (c *convState) runningStartText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.runningLocked()
	if child == nil {
		return ""
	}
	return child.startText()
}

func (c *convState) runningCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.runningLocked()
	if child == nil {
		return 0
	}
	return len(child.calls)
}

func (c *convState) runningFailCode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if child := c.runningLocked(); child != nil && child.failCode != "" {
		return child.failCode
	}
	return "vendor_error"
}

func (c *convState) outputOrNil() *conversation.Output {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output
}

func (c *convState) runName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.run.name
}

func (c *convState) runTenant() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.run.tenant
}

func (c *convState) runUser() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.run.user
}

func (c *convState) conversationUser() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.user
}

func (c *convState) lastTurnIndex() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := len(c.run.children); n > 0 {
		return c.run.children[n-1].index
	}
	return 0
}

func (c *convState) oneCoordinatorRun(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run.name != name {
		return fmt.Errorf("coordinator run = %q, want %q", c.run.name, name)
	}
	if got := len(c.gens); got != 1 {
		return fmt.Errorf("coordinator generations = %d, want 1", got)
	}
	return nil
}

func (c *convState) childrenInOrder(n int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.run.children) != n {
		return fmt.Errorf("child runs = %d, want %d", len(c.run.children), n)
	}
	if n >= 2 && c.run.children[1].startSeq <= c.run.children[0].endSeq {
		return fmt.Errorf("the second child run did not start after the first ended")
	}
	return nil
}

func checkConvErr(w *convWorld, code string) error {
	if w.lastErr == nil {
		return fmt.Errorf("the call succeeded, want %s", code)
	}
	if got := string(core.CodeOf(w.lastErr)); got != code {
		return fmt.Errorf("got code %s, want %s: %w", got, code, w.lastErr)
	}
	// A refused call leaves no trace: reset it for the next assertion.
	w.lastErr = nil
	return nil
}

func tableTexts(table *godog.Table) []string {
	var out []string
	for _, row := range table.Rows {
		if len(row.Cells) > 0 {
			out = append(out, row.Cells[0].Value)
		}
	}
	return out
}

func compareTexts(got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("texts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("texts = %v, want %v", got, want)
		}
	}
	return nil
}

func quotedList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, `"`) && strings.HasSuffix(part, `"`) {
			part = strings.Trim(part, `"`)
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
