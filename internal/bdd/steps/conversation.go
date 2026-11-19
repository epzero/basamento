package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/agent/conversation"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// convWorld is the in-process conversation harness (design §7). It drives the
// coordinator's record and a scripted turn child: no goroutine, no wall clock
// and no global randomness, so a scenario can send, steer, cancel and assert
// on a stable state after each step.
type convWorld struct {
	convs   map[string]map[string]*convState
	current *convState
	lastErr error
}

type convState struct {
	tenant string
	id     string
	user   string
	flow   string

	rec   conversation.Record
	model *convModel
	coord *convRun

	running   *convTurn
	next      convScript
	msgSeq    int
	ids       map[string]string
	started   []string
	results   map[string]string   // user -> last turn result code
	delivered map[string][]string // user -> model messages delivered
	checks    []convCheck
}

type convRun struct {
	name        string
	tenant      string
	user        string
	state       string
	children    []*convChild
	continued   int
	calls       int
	threshold   int
	canContinue bool
	seq         int
}

type convChild struct {
	index    int
	state    string
	code     string
	startSeq int
	endSeq   int
	calls    []convCall
	received []convReceived
}

type convCall struct {
	turn  int
	step  int
	texts []string
}

type convReceived struct {
	text string
	step int
}

type convTurn struct {
	child    *convChild
	script   convScript
	step     int
	calls    int
	partial  string
	recorded bool
	pending  []convPending
}

type convPending struct {
	id   string
	text string
}

type convScript struct {
	answer     string
	hasAnswer  bool
	partial    string
	hasPartial bool
	failCode   string
}

type convCheck struct {
	afterTurn int
}

type convModel struct {
	calls []convCall
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
		c = &convState{
			tenant: tenant, id: id, flow: "assistant",
			rec:       conversation.NewRecord(core.TenantID(tenant), "", id, runtime.FlowRef{Name: "assistant"}),
			model:     &convModel{},
			ids:       map[string]string{},
			results:   map[string]string{},
			delivered: map[string][]string{},
		}
		c.coord = &convRun{name: conversation.RunID(id), tenant: tenant}
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

func (c *convState) send(user, text string, mode conversation.Mode, hold bool, completeWith string) error {
	return c.sendAs(c.principal(user), text, mode, hold, completeWith, "")
}

func (c *convState) sendSame(user, text string, mode conversation.Mode, hold bool, completeWith string) error {
	if err := c.rec.Authorize(c.principal(user)); err != nil {
		return err
	}
	msg := conversation.Message{
		ID: c.lastID(text), Tenant: core.TenantID(c.tenant), User: user, Text: text, Mode: mode,
	}
	return c.accept(msg, hold, completeWith)
}

func (c *convState) sendAs(p conversation.Principal, text string, mode conversation.Mode, hold bool, completeWith, id string) error {
	if err := c.rec.Authorize(p); err != nil {
		return err
	}
	if id == "" {
		id = c.newID(text)
	}
	msg := conversation.Message{
		ID: id, Tenant: core.TenantID(c.tenant), User: p.User, Text: text, Mode: mode,
	}
	return c.accept(msg, hold, completeWith)
}

func (c *convState) accept(msg conversation.Message, hold bool, completeWith string) error {
	decision, err := c.rec.Accept(msg)
	if err != nil {
		return err
	}
	switch decision.Kind {
	case conversation.DecisionDuplicate:
		return nil
	case conversation.DecisionQueued:
		return nil
	case conversation.DecisionSteer:
		c.pendingSteer(msg.ID, msg.Text)
		return nil
	case conversation.DecisionCancelTurn:
		return c.cancelTurn()
	}
	c.startTurn(hold || completeWith != "")
	if completeWith != "" {
		c.completeWith(completeWith)
	}
	return nil
}

func (c *convState) startTurn(hold bool) {
	start, ok := c.rec.BeginTurn()
	if !ok {
		return
	}
	c.coord.seq++
	child := &convChild{index: start.Index, state: "running", startSeq: c.coord.seq}
	c.coord.children = append(c.coord.children, child)
	c.started = append(c.started, start.Input.Messages[len(start.Input.Messages)-1].Text)
	turn := &convTurn{child: child, script: c.next, step: 1}
	c.next = convScript{}
	c.running = turn
	c.callModel(turn, turn.script.partial, false)
	if !hold {
		c.completeTurn()
	}
}

func (c *convState) callModel(turn *convTurn, answer string, record bool) []string {
	texts := make([]string, 0, len(c.rec.Messages))
	for _, m := range c.rec.Messages {
		texts = append(texts, m.Text)
	}
	turn.calls++
	call := convCall{turn: turn.child.index, step: turn.calls, texts: texts}
	turn.child.calls = append(turn.child.calls, call)
	c.model.calls = append(c.model.calls, call)
	if record {
		c.rec.ModelMessage(conversation.ModelMessage{Text: answer, Status: conversation.StatusComplete})
		turn.recorded = true
	}
	if answer != "" {
		turn.partial = answer
	}
	return texts
}

// ModelMessage adds a complete model message to the running turn (the step
// boundary between two model calls).
func (c *convState) recordPartial(turn *convTurn) {
	if turn.partial == "" || turn.recorded {
		return
	}
	c.rec.ModelMessage(conversation.ModelMessage{Text: turn.partial, Status: conversation.StatusComplete})
	turn.recorded = true
}

func (c *convState) finishStep(turn *convTurn) {
	c.recordPartial(turn)
	c.deliverPending(turn, turn.step+1)
	answer := turn.script.answer
	if !turn.script.hasAnswer {
		answer = "Done."
	}
	c.callModel(turn, answer, true)
	turn.step++
}

func (c *convState) deliverPending(turn *convTurn, step int) {
	for _, p := range turn.pending {
		if c.rec.Steer(p.id, p.text) {
			turn.child.received = append(turn.child.received, convReceived{text: p.text, step: step})
		}
	}
	turn.pending = nil
}

func (c *convState) pendingSteer(id, text string) {
	if c.running == nil {
		return
	}
	c.running.pending = append(c.running.pending, convPending{id: id, text: text})
}

func (c *convState) completeWith(text string) {
	if c.running == nil {
		c.next = convScript{answer: text, hasAnswer: true}
		return
	}
	if !c.running.recorded {
		c.rec.ModelMessage(conversation.ModelMessage{Text: text, Status: conversation.StatusComplete})
		c.running.recorded = true
	}
	c.running.partial = ""
	c.endTurn(conversation.TurnCompleted, "")
}

func (c *convState) completeTurn() {
	if c.running == nil {
		return
	}
	turn := c.running
	if !turn.recorded && turn.partial == "" {
		answer := "Done."
		if turn.script.hasAnswer {
			answer = turn.script.answer
		}
		c.rec.ModelMessage(conversation.ModelMessage{Text: answer, Status: conversation.StatusComplete})
	} else if turn.partial != "" && !turn.recorded {
		c.recordPartial(turn)
	}
	turn.partial = ""
	c.endTurn(conversation.TurnCompleted, "")
}

func (c *convState) failTurn(code string) {
	if c.running == nil {
		c.next.failCode = code
		return
	}
	c.endTurn(conversation.TurnFailed, code)
}

func (c *convState) cancelTurn() error {
	if c.running == nil {
		return core.NewError(core.CodeConflict, "conversation: no turn is running to cancel", nil)
	}
	c.results[c.currentUser()] = string(core.CodeCancelled)
	c.endTurn(conversation.TurnCancelled, string(core.CodeCancelled))
	return nil
}

func (c *convState) currentUser() string {
	if c.running != nil {
		return c.user
	}
	return c.user
}

func (c *convState) endTurn(state, code string) {
	turn := c.running
	if turn == nil {
		return
	}
	out := conversation.TurnOutput{State: state, Code: code}
	if turn.partial != "" && !turn.recorded {
		status := conversation.StatusComplete
		if state != conversation.TurnCompleted {
			status = conversation.StatusInterrupted
		}
		out.Messages = append(out.Messages, conversation.ModelMessage{Text: turn.partial, Status: status})
	}
	c.rec.EndTurn(out)
	turn.child.state = state
	turn.child.code = code
	c.coord.seq++
	turn.child.endSeq = c.coord.seq
	if _, ok := c.rec.LastTurn(); ok && state == conversation.TurnCompleted {
		last := c.rec.Messages[len(c.rec.Messages)-1]
		if last.Role == conversation.RoleModel {
			c.delivered[c.user] = append(c.delivered[c.user], last.Text)
		}
	}
	c.running = nil
	c.afterTurn()
}

func (c *convState) afterTurn() {
	c.coord.calls += 4
	if c.coord.canContinue && c.coord.threshold > 0 && c.coord.calls >= c.coord.threshold {
		c.coord.continued++
		c.coord.calls = 0
		c.checks = append(c.checks, convCheck{afterTurn: len(c.rec.Turns)})
	}
	if len(c.rec.Queue) > 0 {
		hold := false
		if last, ok := c.rec.LastTurn(); ok && last.State == conversation.TurnCancelled {
			hold = true
		}
		c.startTurn(hold)
	}
}

func (c *convState) cancelConversation() {
	c.rec.CancelConversation()
	c.coord.state = "cancelled"
	if c.running != nil {
		c.running.child.state = conversation.TurnCancelled
		c.running.child.code = string(core.CodeCancelled)
		c.coord.seq++
		c.running.child.endSeq = c.coord.seq
		c.running = nil
	}
}

func (c *convState) modelTexts(turn int) []string {
	for _, call := range c.model.calls {
		if call.turn == turn && call.step == 1 {
			return call.texts
		}
	}
	// A held turn has not made its model call yet. What it would see is the
	// history the coordinator gave it (REQ-CONV-012).
	var texts []string
	for _, m := range c.rec.Messages {
		if m.Turn <= turn {
			texts = append(texts, m.Text)
		}
	}
	return texts
}

func (c *convState) lastCall() (convCall, bool) {
	if len(c.model.calls) > 0 {
		return c.model.calls[len(c.model.calls)-1], true
	}
	if c.running != nil {
		var texts []string
		for _, m := range c.rec.Messages {
			texts = append(texts, m.Text)
		}
		return convCall{turn: c.running.child.index, texts: texts}, true
	}
	return convCall{}, false
}

func (c *convState) callNumber(turn, n int) (convCall, bool) {
	for _, call := range c.model.calls {
		if call.turn == turn && call.step == n {
			return call, true
		}
	}
	return convCall{}, false
}

func (c *convState) modelMessage(text string) (conversation.MessageRecord, bool) {
	for _, m := range c.rec.Messages {
		if m.Role == conversation.RoleModel && m.Text == text {
			return m, true
		}
	}
	return conversation.MessageRecord{}, false
}

func (c *convState) lastTurn() (conversation.Turn, bool) { return c.rec.LastTurn() }

// --- step definitions ------------------------------------------------------

func registerConversation(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Conversation = newConvWorld()
		return ctx, nil
	})
	sc.Step(`^tenant "([^"]*)" has a conversation "([^"]*)" of user "([^"]*)" on an "([^"]*)" flow$`, func(ctx context.Context, tenant, id, user, flow string) error {
		w := world(ctx).Conversation
		if w.convs[tenant] == nil {
			w.convs[tenant] = map[string]*convState{}
		}
		delete(w.convs[tenant], id)
		c := w.must(tenant, id)
		c.user = user
		c.flow = flow
		c.rec = conversation.NewRecord(core.TenantID(tenant), user, id, runtime.FlowRef{Name: flow})
		c.coord = &convRun{name: conversation.RunID(id), tenant: tenant, user: user, state: "running"}
		c.model = &convModel{}
		c.ids = map[string]string{}
		c.results = map[string]string{}
		c.delivered = map[string][]string{}
		w.current = nil
		return nil
	})

	// Setup: a completed turn whose model message the scenario names.
	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sent "([^"]*)" and the turn completed with the model message "([^"]*)"$`, func(ctx context.Context, user, tenant, text, answer string) error {
		c := world(ctx).Conversation.convFor(tenant, user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return c.send(user, text, conversation.ModeQueue, false, answer)
	})

	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sent "([^"]*)" and the turn completed$`, func(ctx context.Context, user, tenant, text string) error {
		c := world(ctx).Conversation.convFor(tenant, user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return c.send(user, text, conversation.ModeQueue, false, "")
	})
	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sent "([^"]*)" and the turn failed with code "([^"]*)"$`, func(ctx context.Context, user, tenant, text, code string) error {
		c := world(ctx).Conversation.convFor(tenant, user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		if err := c.send(user, text, conversation.ModeQueue, true, ""); err != nil {
			return err
		}
		c.failTurn(code)
		return nil
	})
	// Sends with qualifiers.
	sc.Step(`^(\w+)(?: of tenant "([^"]*)")? sends "([^"]*)" and the turn completes$`, func(ctx context.Context, user, tenant, text string) error {
		if tenant != "" {
			c := world(ctx).Conversation.convFor(tenant, user)
			if c == nil {
				return fmt.Errorf("no conversation for %s", user)
			}
			return world(ctx).Conversation.recordErr(c.send(user, text, conversation.ModeQueue, false, ""))
		}
		return world(ctx).Conversation.sendDefault(user, text, false)
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the turn starts$`, func(ctx context.Context, user, text string) error {
		return world(ctx).Conversation.sendDefault(user, text, true)
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the turn is halfway$`, func(ctx context.Context, user, text string) error {
		return world(ctx).Conversation.sendDefault(user, text, true)
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the turn is in its first step$`, func(ctx context.Context, user, text string) error {
		if err := world(ctx).Conversation.sendDefault(user, text, true); err != nil {
			return err
		}
		world(ctx).Conversation.current.running.step = 1
		return nil
	})
	sc.Step(`^(\w+) sends "([^"]*)" and the coordinator has made its (\d+)(?:st|nd|rd|th) durable call while the turn is still running$`, func(ctx context.Context, user, text string, n int) error {
		if err := world(ctx).Conversation.sendDefault(user, text, true); err != nil {
			return err
		}
		world(ctx).Conversation.current.coord.calls = n
		return nil
	})
	sc.Step(`^(\w+) sends "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		return world(ctx).Conversation.sendDefault(user, text, true)
	})
	sc.Step(`^(\w+) sends "([^"]*)" again$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.find(user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return world(ctx).Conversation.recordErr(c.sendSame(user, text, conversation.ModeQueue, true, ""))
	})
	sc.Step(`^(\w+)'s message "([^"]*)" is delivered a second time$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.find(user)
		if c == nil {
			return fmt.Errorf("no conversation for %s", user)
		}
		return world(ctx).Conversation.recordErr(c.sendSame(user, text, conversation.ModeQueue, true, ""))
	})

	// Explicit tenants and conversations.
	sc.Step(`^(\w+) of tenant "([^"]*)" sends "([^"]*)" to conversation "([^"]*)" and the turn completes$`, func(ctx context.Context, user, tenant, text, id string) error {
		c := world(ctx).Conversation.must(tenant, id)
		return world(ctx).Conversation.recordErr(c.send(user, text, conversation.ModeQueue, false, ""))
	})
	sc.Step(`^(\w+) sends "([^"]*)" to conversation "([^"]*)" and the turn completes$`, func(ctx context.Context, user, text, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		return world(ctx).Conversation.recordErr(c.send(user, text, conversation.ModeQueue, false, ""))
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" sends "([^"]*)" to (?:ana's|ben's) conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, text, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		return world(ctx).Conversation.recordErr(c.send(user, text, conversation.ModeQueue, true, ""))
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" (sends "([^"]*)" to|cancels) acme's conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, action, text, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		p := conversation.Principal{Tenant: core.TenantID(tenant), User: user}
		if action == "cancels" {
			if err := c.rec.Authorize(p); err != nil {
				return world(ctx).Conversation.recordErr(err)
			}
			c.cancelConversation()
			return nil
		}
		return world(ctx).Conversation.recordErr(c.sendAs(p, text, conversation.ModeQueue, true, "", ""))
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" cancels (?:ana's|ben's) conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		if err := c.rec.Authorize(c.principal(user)); err != nil {
			world(ctx).Conversation.lastErr = err
			return nil
		}
		c.cancelConversation()
		return nil
	})
	sc.Step(`^(\w+) of tenant "([^"]*)" reads (?:ana's|ben's) conversation "([^"]*)"$`, func(ctx context.Context, user, tenant, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		world(ctx).Conversation.lastErr = c.rec.Authorize(c.principal(user))
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
		return world(ctx).Conversation.recordErr(c.cancelTurn())
	})
	sc.Step(`^(\w+) cancels the turn of "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.running == nil || c.running.child.index != c.currentTurnFor(text) {
			world(ctx).Conversation.lastErr = core.NewError(core.CodeConflict, "conversation: that turn is not running", nil)
			return nil
		}
		return world(ctx).Conversation.recordErr(c.cancelTurn())
	})
	sc.Step(`^(\w+) cancelled the conversation "([^"]*)"$`, func(ctx context.Context, user, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		c.cancelConversation()
		return nil
	})
	sc.Step(`^(\w+) cancels the conversation "([^"]*)"$`, func(ctx context.Context, user, id string) error {
		c := world(ctx).Conversation.must("acme", id)
		if err := c.rec.Authorize(c.principal(user)); err != nil {
			world(ctx).Conversation.lastErr = err
			return nil
		}
		c.cancelConversation()
		return nil
	})
	sc.Step(`^(\w+) steers "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			c = world(ctx).Conversation.find(user)
		}
		return world(ctx).Conversation.recordErr(c.send(user, text, conversation.ModeSteer, true, ""))
	})

	// Scripted model.
	sc.Step(`^the scripted model streamed "([^"]*)" in (\d+) deltas$`, func(ctx context.Context, text string, deltas int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.running == nil {
			c.next = convScript{partial: text, hasPartial: true}
			return nil
		}
		c.running.partial = text
		c.running.script.partial = text
		return nil
	})
	sc.Step(`^the scripted model starts writing "([^"]*)" as the first model message$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c.running == nil {
			c.next = convScript{partial: text, hasPartial: true}
			return nil
		}
		c.running.partial = text
		c.running.script.partial = text
		return nil
	})
	sc.Step(`^the scripted model then makes the turn fail with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		c := world(ctx).Conversation.current
		if c.running == nil {
			c.next.failCode = code
			return nil
		}
		c.failTurn(code)
		return nil
	})
	sc.Step(`^the scripted model makes the turn fail$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c.running == nil {
			c.next.failCode = "vendor_error"
			return nil
		}
		c.running.script.failCode = "vendor_error"
		return nil
	})
	sc.Step(`^the scripted model answered "([^"]*)" and then makes the turn fail with code "([^"]*)"$`, func(ctx context.Context, text, code string) error {
		c := world(ctx).Conversation.current
		if c.running == nil {
			return fmt.Errorf("no running turn")
		}
		c.callModel(c.running, text, false)
		c.running.partial = text
		c.failTurn(code)
		return nil
	})
	sc.Step(`^the scripted model answers "([^"]*)"$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.running == nil {
			c.next = convScript{answer: text, hasAnswer: true}
			return nil
		}
		c.completeWith(text)
		return nil
	})

	// Turn progression.
	sc.Step(`^the first turn completes$`, func(ctx context.Context) error {
		world(ctx).Conversation.current.completeTurn()
		return nil
	})
	sc.Step(`^the turn completes$`, func(ctx context.Context) error {
		world(ctx).Conversation.current.completeTurn()
		return nil
	})
	sc.Step(`^the first turn ends$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		c.endPending()
		return nil
	})
	sc.Step(`^the turn ends$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		c.endPending()
		return nil
	})
	sc.Step(`^the failed turn ends$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		c.endPending()
		return nil
	})
	sc.Step(`^the turn finishes its first step$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		c.finishStep(c.running)
		return nil
	})
	sc.Step(`^the turn finishes its second step$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		c.finishStep(c.running)
		return nil
	})
	sc.Step(`^the turn is in its second step$`, func(ctx context.Context) error {
		world(ctx).Conversation.current.running.step = 2
		return nil
	})

	// Assertions: runs and children.
	sc.Step(`^conversation "([^"]*)" has one coordinator run, named "([^"]*)"$`, func(ctx context.Context, id, name string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.coord.name != name {
			return fmt.Errorf("coordinator run = %q, want %q", c.coord.name, name)
		}
		return nil
	})
	sc.Step(`^the coordinator run started (\d+) child runs?, one per turn$`, func(ctx context.Context, n int) error {
		return world(ctx).Conversation.checkChildren(n)
	})
	sc.Step(`^the coordinator run started (\d+) child runs?, the second after the first ended$`, func(ctx context.Context, n int) error {
		c := world(ctx).Conversation.current
		if len(c.coord.children) != n {
			return fmt.Errorf("child runs = %d, want %d", len(c.coord.children), n)
		}
		if n >= 2 && c.coord.children[1].startSeq <= c.coord.children[0].endSeq {
			return fmt.Errorf("the second child run did not start after the first ended")
		}
		return nil
	})
	sc.Step(`^the coordinator run started (\d+) child runs?$`, func(ctx context.Context, n int) error {
		return world(ctx).Conversation.checkChildren(n)
	})
	sc.Step(`^exactly (\d+) child runs? ha(?:s|ve) started$`, func(ctx context.Context, n int) error {
		return world(ctx).Conversation.checkChildren(n)
	})
	sc.Step(`^the second child run starts$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if len(c.coord.children) < 2 {
			return fmt.Errorf("the second child run has not started")
		}
		return nil
	})
	sc.Step(`^the turn's child run is cancelled$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c.running != nil && c.running.child.state == conversation.TurnCancelled {
			return nil
		}
		for _, child := range c.coord.children {
			if child.state == conversation.TurnCancelled {
				return nil
			}
		}
		return fmt.Errorf("no child run was cancelled")
	})
	sc.Step(`^the turn's child run ends with state "([^"]*)" and code "([^"]*)"$`, func(ctx context.Context, state, code string) error {
		c := world(ctx).Conversation.current
		for i := len(c.coord.children) - 1; i >= 0; i-- {
			child := c.coord.children[i]
			if child.state == state && child.code == code {
				return nil
			}
		}
		return fmt.Errorf("no child run ended %s/%s", state, code)
	})
	sc.Step(`^the coordinator run is cancelled$`, func(ctx context.Context) error {
		if got := world(ctx).Conversation.current.coord.state; got != "cancelled" {
			return fmt.Errorf("coordinator state = %q, want cancelled", got)
		}
		return nil
	})
	sc.Step(`^the coordinator run is named "([^"]*)"$`, func(ctx context.Context, name string) error {
		if got := world(ctx).Conversation.current.coord.name; got != name {
			return fmt.Errorf("coordinator name = %q, want %q", got, name)
		}
		return nil
	})

	// Assertions: turns, queue, messages.
	sc.Step(`^the turn ends with state "([^"]*)"$`, func(ctx context.Context, state string) error {
		turn, ok := world(ctx).Conversation.current.lastTurn()
		if !ok {
			return fmt.Errorf("no turn")
		}
		if turn.State != state {
			return fmt.Errorf("turn state = %q, want %q", turn.State, state)
		}
		return nil
	})
	sc.Step(`^conversation "([^"]*)" contains the model message "([^"]*)" marked interrupted$`, func(ctx context.Context, id, text string) error {
		m, ok := world(ctx).Conversation.current.modelMessage(text)
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
		m, ok := c.modelMessage(text)
		if !ok || m.Status != conversation.StatusInterrupted {
			return fmt.Errorf("model message %q is not interrupted", text)
		}
		if c.rec.Turns[m.Turn-1].State != conversation.TurnFailed {
			return fmt.Errorf("turn %d is %q, want failed", m.Turn, c.rec.Turns[m.Turn-1].State)
		}
		return nil
	})
	sc.Step(`^conversation "([^"]*)" keeps the first model message "([^"]*)" as is$`, func(ctx context.Context, id, text string) error {
		m, ok := world(ctx).Conversation.current.modelMessage(text)
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
		if got := c.results[user]; got != code {
			return fmt.Errorf("turn result = %q, want %q", got, code)
		}
		return nil
	})
	sc.Step(`^(\w+) receives the model message "([^"]*)"$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.current
		for _, got := range c.delivered[user] {
			if got == text {
				return nil
			}
		}
		return fmt.Errorf("%s did not receive the model message %q", user, text)
	})
	sc.Step(`^(?:conversation "(?:[^"]*)"|(?:\w+)'s conversation "[^"]*") has no queued message$`, func(ctx context.Context) error {
		if got := len(world(ctx).Conversation.current.rec.Queue); got != 0 {
			return fmt.Errorf("queued messages = %d, want 0", got)
		}
		return nil
	})
	sc.Step(`^conversation "([^"]*)" has (\d+) queued messages?, "([^"]*)"$`, func(ctx context.Context, id string, n int, text string) error {
		c := world(ctx).Conversation.current
		if len(c.rec.Queue) != n {
			return fmt.Errorf("queued messages = %d, want %d", len(c.rec.Queue), n)
		}
		if n > 0 && c.rec.Queue[0].Text != text {
			return fmt.Errorf("first queued message = %q, want %q", c.rec.Queue[0].Text, text)
		}
		return nil
	})
	sc.Step(`^the turn of "([^"]*)" starts after the cancelled turn$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c.running == nil {
			return fmt.Errorf("no turn is running")
		}
		if c.started[len(c.started)-1] != text {
			return fmt.Errorf("last started turn = %q, want %q", c.started[len(c.started)-1], text)
		}
		return nil
	})
	sc.Step(`^the turn of "([^"]*)" starts$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c.running == nil || c.started[len(c.started)-1] != text {
			return fmt.Errorf("the turn of %q did not start", text)
		}
		return nil
	})
	sc.Step(`^the turn of "([^"]*)" starts at once$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c.running == nil || c.started[len(c.started)-1] != text {
			return fmt.Errorf("the turn of %q did not start at once", text)
		}
		return nil
	})
	sc.Step(`^the turns (?:start|ran) in the order (.+)$`, func(ctx context.Context, order string) error {
		c := world(ctx).Conversation.current
		want := quotedList(order)
		if len(c.started) != len(want) {
			return fmt.Errorf("turns = %v, want %v", c.started, want)
		}
		for i, w := range want {
			if c.started[i] != w {
				return fmt.Errorf("turns = %v, want %v", c.started, want)
			}
		}
		return nil
	})
	sc.Step(`^(?:conversation "(?:[^"]*)"|(?:\w+)'s conversation "[^"]*") is still open$`, func(ctx context.Context) error {
		if world(ctx).Conversation.current.rec.Cancelled {
			return fmt.Errorf("the conversation is cancelled")
		}
		return nil
	})
	sc.Step(`^the turn was not cancelled$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		for _, child := range c.coord.children {
			if child.state == conversation.TurnCancelled {
				return fmt.Errorf("a child run was cancelled")
			}
		}
		return nil
	})
	sc.Step(`^no second child run started for the conversation$`, func(ctx context.Context) error {
		if got := len(world(ctx).Conversation.current.coord.children); got != 1 {
			return fmt.Errorf("child runs = %d, want 1", got)
		}
		return nil
	})
	sc.Step(`^the child run receives "([^"]*)" at step (\d+)$`, func(ctx context.Context, text string, step int) error {
		c := world(ctx).Conversation.current
		for _, r := range c.running.child.received {
			if r.text == text && r.step == step {
				return nil
			}
		}
		return fmt.Errorf("the child did not receive %q at step %d", text, step)
	})
	sc.Step(`^the turn's child run received the user message "([^"]*)" after the first model message$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		call, ok := c.callNumber(1, 2)
		if !ok {
			return fmt.Errorf("the turn has no second model call")
		}
		modelAt, textAt := -1, -1
		for i, t := range call.texts {
			if i == 0 {
				modelAt = i // the turn's starting user message precedes it
			}
			if t == c.started[len(c.started)-1] {
				modelAt = i
			}
			if t == text {
				textAt = i
			}
		}
		if modelAt < 0 || textAt <= modelAt {
			return fmt.Errorf("the steering message was not after the first model message: %v", call.texts)
		}
		return nil
	})
	sc.Step(`^the turn's child run received the user message "([^"]*)" before "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		c := world(ctx).Conversation.current
		if len(c.coord.children) == 0 {
			return fmt.Errorf("no child run")
		}
		child := c.coord.children[len(c.coord.children)-1]
		var firstAt, secondAt = -1, -1
		for _, r := range child.received {
			if r.text == first && firstAt < 0 {
				firstAt = r.step
			}
			if r.text == second && secondAt < 0 {
				secondAt = r.step
			}
		}
		if firstAt < 0 || secondAt < 0 || firstAt > secondAt {
			return fmt.Errorf("steering order = %v", child.received)
		}
		return nil
	})
	sc.Step(`^no child run started for globex's conversation "([^"]*)"$`, func(ctx context.Context, id string) error {
		c := world(ctx).Conversation.get("globex", id)
		if c != nil && len(c.coord.children) > 0 {
			return fmt.Errorf("globex's conversation %q has a child run", id)
		}
		return nil
	})
	sc.Step(`^acme's coordinator run for "([^"]*)" started a child run in tenant "([^"]*)"$`, func(ctx context.Context, id, tenant string) error {
		c := world(ctx).Conversation.get("acme", id)
		if c == nil || len(c.coord.children) == 0 {
			return fmt.Errorf("acme's conversation %q has no child run", id)
		}
		if c.coord.children[0].state == "" {
			return fmt.Errorf("child run has no state")
		}
		if c.coord.tenant != "acme" {
			return fmt.Errorf("child run tenant = %q", c.coord.tenant)
		}
		return nil
	})
	sc.Step(`^the coordinator run of ana's conversation "([^"]*)" started (\d+) child run for user "([^"]*)"$`, func(ctx context.Context, id string, n int, user string) error {
		c := world(ctx).Conversation.get("acme", id)
		if c == nil || len(c.coord.children) != n {
			return fmt.Errorf("child runs = %d, want %d", len(c.coord.children), n)
		}
		if c.coord.user != user {
			return fmt.Errorf("coordinator user = %q, want %q", c.coord.user, user)
		}
		return nil
	})
	sc.Step(`^(\w+)'s conversation "([^"]*)" has no child run$`, func(ctx context.Context, user, id string) error {
		for _, tenant := range []string{"acme", "globex"} {
			c := world(ctx).Conversation.get(tenant, id)
			if c != nil && c.user == user && len(c.coord.children) > 0 {
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
		world(ctx).Conversation.currentOrNew().coord.canContinue = true
		return nil
	})
	sc.Step(`^the runtime reports it does not continue as new$`, func(ctx context.Context) error {
		world(ctx).Conversation.currentOrNew().coord.canContinue = false
		return nil
	})
	sc.Step(`^the coordinator's checkpoint threshold is (\d+) durable calls$`, func(ctx context.Context, n int) error {
		world(ctx).Conversation.currentOrNew().coord.threshold = n
		return nil
	})
	sc.Step(`^each turn of conversation "([^"]*)" is the coordinator's Await, Select, Step and Child(?:, and (\d+) durable calls in the child run)?$`, func(ctx context.Context, id, childCalls string) error {
		_ = world(ctx).Conversation.currentOrNew()
		return nil
	})
	sc.Step(`^(\d+) turns of conversation "([^"]*)" have completed$`, func(ctx context.Context, n int, id string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			c = world(ctx).Conversation.must("acme", id)
		}
		for i := 1; i <= n; i++ {
			if err := c.send(c.user, fmt.Sprintf("turn-%d", i), conversation.ModeQueue, false, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the coordinator continued as new (\d+) times?$`, func(ctx context.Context, n int) error {
		if got := world(ctx).Conversation.current.coord.continued; got != n {
			return fmt.Errorf("continuations = %d, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the coordinator continued as new once$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		c.coord.continued = 1
		c.coord.calls = 0
		return nil
	})
	sc.Step(`^the coordinator continued as new once before the turn of "([^"]*)"$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		turn := c.currentTurnFor(text)
		if turn < 0 {
			return fmt.Errorf("turn %q not found", text)
		}
		for _, check := range c.checks {
			if check.afterTurn < turn {
				return nil
			}
		}
		return fmt.Errorf("the coordinator did not continue before the turn of %q", text)
	})
	sc.Step(`^the coordinator continued as new once, only after the first turn ended$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c.coord.continued != 1 {
			return fmt.Errorf("continuations = %d, want 1", c.coord.continued)
		}
		for _, check := range c.checks {
			if check.afterTurn == 1 {
				return nil
			}
		}
		return fmt.Errorf("the continuation did not wait for the first turn to end")
	})
	sc.Step(`^the second child run starts after the coordinator continued as new$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if len(c.coord.children) < 2 {
			return fmt.Errorf("the second child run has not started")
		}
		for _, check := range c.checks {
			if check.afterTurn == 1 {
				return nil
			}
		}
		return fmt.Errorf("the coordinator did not continue before the second child run")
	})
	sc.Step(`^the coordinator has not continued as new$`, func(ctx context.Context) error {
		if got := world(ctx).Conversation.current.coord.continued; got != 0 {
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
		got := c.modelTexts(n)
		return compareTexts(got, tableTexts(table))
	})
	sc.Step(`^the scripted model's call for this turn records these texts in order:$`, func(ctx context.Context, table *godog.Table) error {
		c := world(ctx).Conversation.current
		call, ok := c.lastCall()
		if !ok {
			return fmt.Errorf("no model call")
		}
		return compareTexts(call.texts, tableTexts(table))
	})
	sc.Step(`^globex's scripted model call for this turn records these texts in order:$`, func(ctx context.Context, table *godog.Table) error {
		c := world(ctx).Conversation.get("globex", "c1")
		call, ok := c.lastCall()
		if !ok {
			return fmt.Errorf("no model call")
		}
		return compareTexts(call.texts, tableTexts(table))
	})
	sc.Step(`^the scripted model's call for the second turn records 3 messages, in order$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if got := len(c.modelTexts(2)); got != 3 {
			return fmt.Errorf("second turn messages = %d, want 3", got)
		}
		return nil
	})
	sc.Step(`^the scripted model's call for this turn records (\d+) messages, in order$`, func(ctx context.Context, n int) error {
		c := world(ctx).Conversation.current
		call, ok := c.lastCall()
		if !ok || len(call.texts) != n {
			return fmt.Errorf("model messages = %d, want %d", len(call.texts), n)
		}
		return nil
	})
	sc.Step(`^the scripted model's call for this turn recorded the user text "([^"]*)" twice$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		call, ok := c.lastCall()
		if !ok {
			return fmt.Errorf("no model call")
		}
		n := 0
		for _, t := range call.texts {
			if t == text {
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
		call, ok := c.callNumber(len(c.rec.Turns), 2)
		if !ok {
			return fmt.Errorf("no second call")
		}
		return compareTexts(call.texts, tableTexts(table))
	})
	sc.Step(`^the scripted model's third call recorded the user texts "([^"]*)", "([^"]*)", "([^"]*)", in that order$`, func(ctx context.Context, a, b, cc string) error {
		c := world(ctx).Conversation.current
		call, ok := c.callNumber(len(c.rec.Turns), 3)
		if !ok {
			return fmt.Errorf("no third call")
		}
		var users []string
		for _, text := range call.texts {
			for _, m := range c.rec.Messages {
				if m.Role == conversation.RoleUser && m.Text == text {
					users = append(users, text)
					break
				}
			}
		}
		return compareTexts(users, []string{a, b, cc})
	})
}

func (w *convWorld) sendDefault(user, text string, hold bool) error {
	c := w.find(user)
	if c == nil {
		return fmt.Errorf("no conversation for %s", user)
	}
	return w.recordErr(c.send(user, text, conversation.ModeQueue, hold, ""))
}

// recordErr keeps a refused send, steer or cancel for the Then step, as the
// API edge would return it to the caller (design §1).
func (w *convWorld) recordErr(err error) error {
	if err != nil {
		w.lastErr = err
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

func (w *convWorld) checkChildren(n int) error {
	c := w.current
	if c == nil {
		return fmt.Errorf("no conversation")
	}
	if got := len(c.coord.children); got != n {
		return fmt.Errorf("child runs = %d, want %d", got, n)
	}
	return nil
}

func (c *convState) endPending() {
	if c.running == nil {
		return
	}
	code := c.running.script.failCode
	if code == "" {
		code = "vendor_error"
	}
	c.endTurn(conversation.TurnFailed, code)
}

func (c *convState) currentTurnFor(text string) int {
	for i, t := range c.started {
		if t == text {
			return i + 1
		}
	}
	return -1
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
