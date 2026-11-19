package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/agent/conversation"
	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/stream"
	"github.com/epzero/basamento/stream/eventlog"
)

// convDurable is what the conversation's data directory keeps across a
// simulated process kill: the coordinator's record (which carries the running
// turn and the queue) and the identity of the in-flight model message.
type convDurable struct {
	Record    conversation.Record `json:"record"`
	MsgID     string              `json:"msg_id,omitempty"`
	Attempt   int                 `json:"attempt,omitempty"`
	Results   map[string]string   `json:"results,omitempty"`
	Delivered map[string][]string `json:"delivered,omitempty"`
}

// registerConversationCrash registers the durable-runtime harness (WP-L7-04).
// The stream and the record are written under the scenario's data directory:
// killing the harness drops the in-memory coordinator, and starting it again on
// the same directory resumes the record, the running turn and the queue.
func registerConversationCrash(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Conversation; w != nil {
			w.teardownDurable()
		}
		return ctx, nil
	})

	sc.Step(`^tenant "([^"]*)" has a conversation "([^"]*)" of user "([^"]*)" on an "([^"]*)" flow on the temporal runtime$`, func(ctx context.Context, tenant, id, user, flow string) error {
		c := world(ctx).Conversation.must(tenant, id)
		c.user = user
		c.flow = flow
		c.runtimeName = "temporal"
		c.rec = conversation.NewRecord(core.TenantID(tenant), user, id, runtime.FlowRef{Name: flow})
		c.coord = &convRun{name: conversation.RunID(id), tenant: tenant, user: user, state: "running"}
		c.ids = map[string]string{}
		c.results = map[string]string{}
		c.delivered = map[string][]string{}
		c.turnAnswers = map[int]string{}
		c.clock = world(ctx).Core.clock
		return c.openDurable()
	})

	sc.Step(`^(\w+) sends "([^"]*)" and attaches to the stream$`, func(ctx context.Context, user, text string) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if err := c.send(user, text, conversation.ModeQueue, true, ""); err != nil {
			return err
		}
		return c.attachStream()
	})

	sc.Step(`^the process running conversation "([^"]*)" is killed and started again on the same data directory$`, func(ctx context.Context, id string) error {
		c := world(ctx).Conversation.get("acme", id)
		if c == nil {
			return fmt.Errorf("no conversation %q", id)
		}
		if err := c.kill(); err != nil {
			return err
		}
		return c.restart()
	})

	sc.Step(`^the scripted model streams "([^"]*)" in (\d+) deltas and the turn completes$`, func(ctx context.Context, text string, deltas int) error {
		c := world(ctx).Conversation.current
		if c == nil || c.running == nil {
			return fmt.Errorf("no running turn")
		}
		if err := c.streamAttempt(text, deltas, c.attempt > 1); err != nil {
			return err
		}
		c.completeWith(text)
		return nil
	})

	sc.Step(`^the scripted model answers "([^"]*)" for turn (\d+), "([^"]*)" for turn (\d+), and "([^"]*)" for turn (\d+)$`, func(ctx context.Context, first string, firstTurn int, second string, secondTurn int, third string, thirdTurn int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		if c.turnAnswers == nil {
			c.turnAnswers = map[int]string{}
		}
		c.turnAnswers[firstTurn] = first
		c.turnAnswers[secondTurn] = second
		c.turnAnswers[thirdTurn] = third
		return nil
	})

	sc.Step(`^all (\d+) turns complete$`, func(ctx context.Context, n int) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		for completed := 0; completed < n; completed++ {
			if c.running == nil {
				return fmt.Errorf("turn %d did not start: %d of %d completed", completed+1, completed, n)
			}
			turn := c.running.child.index
			answer, ok := c.turnAnswers[turn]
			if !ok {
				answer = "Done."
			}
			if err := c.streamAttempt(answer, 3, c.attempt > 1); err != nil {
				return err
			}
			c.completeWith(answer)
		}
		if c.running != nil || len(c.rec.Queue) != 0 {
			return fmt.Errorf("after %d turns: running=%v queue=%d, want every turn complete", n, c.running != nil, len(c.rec.Queue))
		}
		return nil
	})

	sc.Step(`^conversation "([^"]*)" holds these messages in order:$`, func(ctx context.Context, id string, rows *godog.Table) error {
		c := world(ctx).Conversation.get("acme", id)
		if c == nil {
			return fmt.Errorf("no conversation %q", id)
		}
		return c.holdsMessages(rows)
	})

	sc.Step(`^the turn completes with the model message "([^"]*)"$`, func(ctx context.Context, text string) error {
		c := world(ctx).Conversation.current
		if c == nil || c.running == nil {
			return fmt.Errorf("no running turn")
		}
		c.completeWith(text)
		return nil
	})

	sc.Step(`^the recording step of the first turn runs again for the same model message$`, func(ctx context.Context) error {
		c := world(ctx).Conversation.current
		if c == nil {
			return fmt.Errorf("no conversation")
		}
		for _, m := range c.rec.Messages {
			if m.Turn == 1 && m.Role == conversation.RoleModel {
				c.rec.RecordModelMessage(m.Turn, m.Position, conversation.ModelMessage{Text: m.Text, Status: m.Status})
				return nil
			}
		}
		return fmt.Errorf("the first turn recorded no model message")
	})
}

// --- durable harness -------------------------------------------------------

func (c *convState) tenantCtx() context.Context {
	return core.WithTenant(context.Background(), core.TenantID(c.tenant))
}

func (c *convState) runID() string { return conversation.RunID(c.id) }

// openDurable opens the SQL store, the event log and the broker in the
// scenario's data directory. It is idempotent, and after a simulated restart it
// finds the run and its messages where the killed process left them.
func (c *convState) openDurable() error {
	if c.store != nil {
		return nil
	}
	if c.dir == "" {
		dir, err := os.MkdirTemp("", "conv-crash-*")
		if err != nil {
			return err
		}
		c.dir = dir
	}
	if c.clock == nil {
		c.clock = core.SystemClock{}
	}
	store, err := psql.Open(context.Background(), "sqlite:///"+filepath.Join(c.dir, "kit.db"), eventlog.Migrations())
	if err != nil {
		return err
	}
	c.store = store
	lg, err := eventlog.New(store, eventlog.Options{Clock: c.clock})
	if err != nil {
		return err
	}
	c.log = lg
	broker, err := stream.NewBroker(lg, &deltaBus{}, stream.BrokerOptions{Clock: c.clock})
	if err != nil {
		return err
	}
	c.broker = broker
	tc := c.tenantCtx()
	if _, err := lg.State(tc, c.runID()); core.CodeOf(err) == core.CodeNotFound {
		if err := lg.CreateRun(tc, c.runID(), "running"); err != nil {
			return err
		}
	}
	return broker.Open(tc, c.runID(), stream.RunOptions{})
}

func (c *convState) closeDurable() {
	if c.store != nil {
		_ = c.store.Close()
	}
	c.store = nil
	c.log = nil
	c.broker = nil
}

func (c *convState) save() error {
	if c.dir == "" {
		return nil
	}
	data := convDurable{
		Record:    c.rec,
		MsgID:     c.msgID,
		Attempt:   c.attempt,
		Results:   c.results,
		Delivered: c.delivered,
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.dir, "conversation.json"), raw, 0o600)
}

func (c *convState) load() (convDurable, error) {
	raw, err := os.ReadFile(filepath.Join(c.dir, "conversation.json"))
	if err != nil {
		return convDurable{}, err
	}
	var data convDurable
	return data, json.Unmarshal(raw, &data)
}

// kill stops the coordinator the scene runs: the client keeps its cursor, the
// record and the stream stay in the data directory, and every in-memory turn
// is lost as it would be with a SIGKILL.
func (c *convState) kill() error {
	if err := c.save(); err != nil {
		return err
	}
	if c.client != nil {
		c.cursor = c.client.Cursor()
		_ = c.client.Close()
		c.client = nil
	}
	c.closeDurable()
	c.running = nil
	c.killed = true
	c.coord.state = "killed"
	return nil
}

// restart starts the coordinator again on the same data directory. The record
// keeps the running turn and the queue; the in-flight model message becomes
// attempt 2.
func (c *convState) restart() error {
	data, err := c.load()
	if err != nil {
		return err
	}
	c.rec = data.Record
	c.results = data.Results
	if c.results == nil {
		c.results = map[string]string{}
	}
	c.delivered = data.Delivered
	if c.delivered == nil {
		c.delivered = map[string][]string{}
	}
	c.msgID = data.MsgID
	c.attempt = data.Attempt + 1
	if c.attempt < 2 {
		c.attempt = 2
	}
	c.killed = false
	c.coord.state = "running"
	if err := c.openDurable(); err != nil {
		return err
	}
	if c.rec.Running > 0 {
		c.coord.seq++
		child := &convChild{index: c.rec.Running, state: "running", startSeq: c.coord.seq}
		c.coord.children = append(c.coord.children, child)
		c.running = &convTurn{child: child, step: 1}
		if c.msgID != "" {
			if err := c.broker.StartMessage(c.tenantCtx(), c.runID(), c.msgID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *convState) attachStream() error {
	if c.runtimeName != "temporal" {
		return nil
	}
	if err := c.openDurable(); err != nil {
		return err
	}
	att, err := c.broker.Attach(c.tenantCtx(), c.runID(), stream.Cursor{})
	if err != nil {
		return err
	}
	c.client = att
	c.drain()
	return nil
}

func (c *convState) reattachStream() error {
	if c.runtimeName != "temporal" {
		return fmt.Errorf("conversation %q is not on the temporal runtime", c.id)
	}
	if err := c.openDurable(); err != nil {
		return err
	}
	att, err := c.broker.Attach(c.tenantCtx(), c.runID(), c.cursor)
	if err != nil {
		return err
	}
	c.client = att
	c.drain()
	return nil
}

func (c *convState) drain() {
	if c.client == nil {
		return
	}
	for {
		frame, ok := c.client.TryNext()
		if !ok {
			return
		}
		c.frames = append(c.frames, frame)
	}
}

// startTemporalMessage opens the stream message of a turn that just began.
func (c *convState) startTemporalMessage(turn int) error {
	if c.runtimeName != "temporal" {
		return nil
	}
	if err := c.openDurable(); err != nil {
		return err
	}
	c.msgID = fmt.Sprintf("%s-t%d-m1", c.id, turn)
	c.attempt = 1
	return c.broker.StartMessage(c.tenantCtx(), c.runID(), c.msgID)
}

// streamDeltas publishes one model attempt's deltas without completing the
// message, so a kill can interrupt an attempt mid-stream.
func (c *convState) streamDeltas(text string, deltas int, reset bool) error {
	if c.runtimeName != "temporal" {
		return nil
	}
	tc := c.tenantCtx()
	if reset {
		if _, err := c.broker.Reset(tc, c.runID(), c.msgID, c.attempt); err != nil {
			return err
		}
	}
	for _, chunk := range splitRunes(text, deltas) {
		if err := c.broker.Heartbeat(tc, c.runID()); err != nil {
			return err
		}
		if _, err := c.broker.Publish(tc, c.runID(), stream.Delta{
			MessageID: c.msgID,
			Kind:      "text",
			Text:      chunk,
			Attempt:   c.attempt,
		}); err != nil {
			return err
		}
	}
	c.drain()
	return nil
}

// streamAttempt publishes one model attempt's deltas and completes the message.
func (c *convState) streamAttempt(text string, deltas int, reset bool) error {
	if c.runtimeName != "temporal" {
		return nil
	}
	if err := c.streamDeltas(text, deltas, reset); err != nil {
		return err
	}
	if err := c.broker.CompleteMessage(c.tenantCtx(), c.runID(), c.msgID, false); err != nil {
		return err
	}
	c.drain()
	return nil
}

// holdsMessages matches the scenario table against the conversation history in
// order, role and text.
func (c *convState) holdsMessages(rows *godog.Table) error {
	if len(rows.Rows) < 2 {
		return fmt.Errorf("the table needs a header and at least one row")
	}
	want := rows.Rows[1:]
	if len(c.rec.Messages) != len(want) {
		return fmt.Errorf("conversation %q holds %d messages, want %d: %+v", c.id, len(c.rec.Messages), len(want), c.rec.Messages)
	}
	for i, row := range want {
		role, text := row.Cells[0].Value, row.Cells[1].Value
		got := c.rec.Messages[i]
		if got.Role != role || got.Text != text {
			return fmt.Errorf("message %d = (%s, %q), want (%s, %q)", i, got.Role, got.Text, role, text)
		}
	}
	return nil
}

// streamHasTextOnce is the conversation's view of REQ-STRM-002: the completed
// message text appears once, after any reset cleared the interrupted attempt.
func (c *convState) streamHasTextOnce(text string) error {
	completions := 0
	var after strings.Builder
	for _, f := range c.frames {
		switch {
		case f.Durable != nil && f.Durable.Type == "message.completed":
			var data struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(f.Durable.Data, &data); err == nil && data.Text == text {
				completions++
			}
		case f.Snapshot != nil:
			after.WriteString(f.Snapshot.Text)
		case f.Delta != nil && f.Delta.Kind == "reset":
			after.Reset()
		case f.Delta != nil && f.Delta.Kind == "text":
			after.WriteString(f.Delta.Text)
		}
	}
	if completions != 1 {
		return fmt.Errorf("%s's stream recorded %q as a completed message %d times, want exactly once", c.user, text, completions)
	}
	if got := strings.Count(after.String(), text); got != 1 {
		return fmt.Errorf("%s's stream contains %q %d times after the reset, want exactly once", c.user, text, got)
	}
	return nil
}

// resetBeforeAttempt checks that the reset of attempt came before its deltas.
func (c *convState) resetBeforeAttempt(attempt int) error {
	resetAt, firstAt := -1, -1
	for i, f := range c.frames {
		if f.Delta == nil {
			continue
		}
		if f.Delta.Kind == "reset" && f.Delta.Attempt == attempt && resetAt < 0 {
			resetAt = i
		}
		if f.Delta.Kind == "text" && f.Delta.Attempt == attempt && firstAt < 0 {
			firstAt = i
		}
	}
	if resetAt < 0 {
		return fmt.Errorf("%s received no reset for attempt %d", c.user, attempt)
	}
	if firstAt < 0 {
		return fmt.Errorf("%s received no delta of attempt %d", c.user, attempt)
	}
	if resetAt > firstAt {
		return fmt.Errorf("the attempt %d delta came before the reset", attempt)
	}
	return nil
}

// temporalFor returns the durable conversation the named user is driving, or
// nil when the scenario is a stream-only one.
func (w *convWorld) temporalFor(user string) *convState {
	for _, byID := range w.convs {
		for _, c := range byID {
			if c.runtimeName == "temporal" && c.user == user {
				return c
			}
		}
	}
	return nil
}

func (w *convWorld) teardownDurable() {
	for _, byID := range w.convs {
		for _, c := range byID {
			if c.client != nil {
				_ = c.client.Close()
				c.client = nil
			}
			c.closeDurable()
			if c.dir != "" {
				_ = os.RemoveAll(c.dir)
				c.dir = ""
			}
		}
	}
}
