package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
	"github.com/epzero/basamento/stream"
	"github.com/epzero/basamento/stream/eventlog"
)

var streamNow = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

// streamWorld drives the live layer for the detach and cancellation scenarios:
// a scripted model streams deltas through the broker, clients attach and
// detach, and the scenario clock moves detach deadlines without sleeping.
type streamWorld struct {
	dir   string
	store *psql.Store
	log   *eventlog.Log
	clock *coretest.FakeClock
	bus   *deltaBus
	live  *stream.Broker

	text   string
	chunks []string
	sent   int

	failing      bool
	failAfter    int
	retryText    string
	retryChunks  []string
	retrySent    int
	retryStarted bool

	detachAfter time.Duration

	runID     string
	messageID string
	clients   map[string]*streamClient
	started   bool
}

type streamClient struct {
	att          *stream.Attachment
	frames       []stream.Frame
	mark         int
	detachCursor stream.Cursor
}

func newStreamWorld() *streamWorld {
	return &streamWorld{
		clients:   map[string]*streamClient{},
		messageID: "m1",
		runID:     "r1",
	}
}

func scenarioUsesStream(scn *godog.Scenario) bool {
	switch filepath.Base(scn.Uri) {
	case "detach-reattach.feature", "retries-and-cancellation.feature":
		return true
	}
	return false
}

func (w *streamWorld) tenantCtx() context.Context {
	return core.WithTenant(context.Background(), core.TenantID("acme"))
}

// open prepares the SQL store, the log and the broker of the scenario.
func (w *streamWorld) open(ctx context.Context) error {
	if w.store != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "stream-sqlite-*")
	if err != nil {
		return err
	}
	store, err := psql.Open(ctx, "sqlite:///"+filepath.Join(dir, "kit.db"), eventlog.Migrations())
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	w.dir = dir
	w.store = store
	w.clock = coretest.NewFakeClock(streamNow)
	w.log, err = eventlog.New(store, eventlog.Options{Clock: w.clock})
	if err != nil {
		return err
	}
	w.bus = &deltaBus{}
	w.live, err = stream.NewBroker(w.log, w.bus, stream.BrokerOptions{Clock: w.clock})
	return err
}

func (w *streamWorld) teardown() {
	for _, c := range w.clients {
		if c.att != nil {
			_ = c.att.Close()
		}
	}
	if w.store != nil {
		_ = w.store.Close()
		w.store = nil
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
		w.dir = ""
	}
}

func (w *streamWorld) script(text string, deltas int) error {
	if deltas <= 0 {
		return fmt.Errorf("scripted model needs at least one delta")
	}
	if text == "" {
		return fmt.Errorf("scripted model needs text")
	}
	w.text = text
	w.chunks = splitRunes(text, deltas)
	w.sent = 0
	w.failing = false
	w.failAfter = 0
	w.retryText = ""
	w.retryChunks = nil
	return nil
}

func (w *streamWorld) failingScript(failAfter int, text string, deltas int) error {
	if failAfter <= 0 || deltas <= 0 {
		return fmt.Errorf("scripted failure needs positive deltas")
	}
	w.text = "partial output"
	w.chunks = splitRunes(w.text, failAfter)
	w.sent = 0
	w.failing = true
	w.failAfter = failAfter
	w.retryText = text
	w.retryChunks = splitRunes(text, deltas)
	w.retrySent = 0
	return nil
}

// splitRunes cuts text into deltas chunks, the first ones one rune longer when
// it does not divide evenly.
func splitRunes(text string, deltas int) []string {
	runes := []rune(text)
	if deltas > len(runes) {
		deltas = len(runes)
	}
	base, extra := len(runes)/deltas, len(runes)%deltas
	out := make([]string, 0, deltas)
	at := 0
	for i := 0; i < deltas; i++ {
		n := base
		if i < extra {
			n++
		}
		out = append(out, string(runes[at:at+n]))
		at += n
	}
	return out
}

func (w *streamWorld) start(ctx context.Context, user string, deltas int) error {
	if err := w.open(ctx); err != nil {
		return err
	}
	if w.started {
		return fmt.Errorf("the run already started")
	}
	if len(w.chunks) == 0 {
		return fmt.Errorf("no scripted model")
	}
	ctx = w.tenantCtx()
	if err := w.log.CreateRun(ctx, w.runID, "running"); err != nil {
		return err
	}
	if err := w.live.Open(ctx, w.runID, stream.RunOptions{DetachAfter: w.detachAfter}); err != nil {
		return err
	}
	if _, err := w.live.AppendChange(ctx, w.runID, "running", []eventlog.NewEvent{
		{Type: "run.started"},
		{Type: "message.started", Data: json.RawMessage(`{"message_id":"` + w.messageID + `"}`)},
	}); err != nil {
		return err
	}
	if err := w.live.StartMessage(ctx, w.runID, w.messageID); err != nil {
		return err
	}
	w.started = true
	if err := w.attach(user); err != nil {
		return err
	}
	return w.stream(ctx, deltas)
}

func (w *streamWorld) attach(user string) error {
	att, err := w.live.Attach(w.tenantCtx(), w.runID, stream.Cursor{})
	if err != nil {
		return err
	}
	w.clients[user] = &streamClient{att: att}
	return nil
}

func (w *streamWorld) reattach(user string) error {
	c, ok := w.clients[user]
	if !ok {
		return fmt.Errorf("no client %q", user)
	}
	att, err := w.live.Attach(w.tenantCtx(), w.runID, c.detachCursor)
	if err != nil {
		return err
	}
	c.att = att
	c.mark = len(c.frames)
	return nil
}

// stream publishes n more of the first attempt's deltas.
func (w *streamWorld) stream(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		if w.sent >= len(w.chunks) {
			return fmt.Errorf("the scripted model has only %d deltas", len(w.chunks))
		}
		if err := w.live.Heartbeat(ctx, w.runID); err != nil {
			return err
		}
		if _, err := w.live.Publish(ctx, w.runID, stream.Delta{
			MessageID: w.messageID,
			Kind:      "text",
			Text:      w.chunks[w.sent],
			Attempt:   w.attempt(),
		}); err != nil {
			return err
		}
		w.sent++
	}
	w.drain()
	return nil
}

func (w *streamWorld) attempt() int {
	if w.failing {
		return 1
	}
	return 1
}

// finish streams whatever is left, completes the message and the run. A failed
// first attempt resets the message and streams the second attempt first.
func (w *streamWorld) finish(context.Context) error {
	ctx := w.tenantCtx()
	if w.failing && !w.retryStarted {
		if _, err := w.live.Reset(ctx, w.runID, w.messageID, 2); err != nil {
			return err
		}
		w.retryStarted = true
		for _, chunk := range w.retryChunks {
			if err := w.live.Heartbeat(ctx, w.runID); err != nil {
				return err
			}
			if _, err := w.live.Publish(ctx, w.runID, stream.Delta{
				MessageID: w.messageID,
				Kind:      "text",
				Text:      chunk,
				Attempt:   2,
			}); err != nil {
				return err
			}
			w.retrySent++
		}
	} else {
		if err := w.stream(ctx, len(w.chunks)-w.sent); err != nil {
			return err
		}
	}
	if err := w.live.CompleteMessage(ctx, w.runID, w.messageID, false); err != nil {
		return err
	}
	if _, err := w.live.AppendChange(ctx, w.runID, "completed", []eventlog.NewEvent{{Type: "run.completed"}}); err != nil {
		return err
	}
	if err := w.live.Finish(ctx, w.runID); err != nil {
		return err
	}
	w.drain()
	return nil
}

func (w *streamWorld) detach(user string) error {
	c, ok := w.clients[user]
	if !ok {
		return fmt.Errorf("no client %q", user)
	}
	c.detachCursor = c.att.Cursor()
	return c.att.Close()
}

func (w *streamWorld) cancel(user string) error {
	if _, ok := w.clients[user]; !ok {
		return fmt.Errorf("no client %q", user)
	}
	ctx := w.tenantCtx()
	if err := w.live.Cancel(ctx, w.runID); err != nil {
		return err
	}
	w.drain()
	return nil
}

func (w *streamWorld) advance(d time.Duration) error {
	w.clock.Advance(d)
	_, err := w.live.EnforceDetachPolicies(w.tenantCtx())
	return err
}

// drain takes every buffered frame of every client. Frames arrive
// synchronously as the model streams, so there is nothing to wait for.
func (w *streamWorld) drain() {
	for _, c := range w.clients {
		for {
			frame, ok := c.att.TryNext()
			if !ok {
				break
			}
			c.frames = append(c.frames, frame)
		}
	}
}

func (w *streamWorld) client(user string) (*streamClient, error) {
	c, ok := w.clients[user]
	if !ok {
		return nil, fmt.Errorf("no client %q", user)
	}
	return c, nil
}

func (w *streamWorld) durable(user string) []eventlog.Event {
	c, err := w.client(user)
	if err != nil {
		return nil
	}
	var out []eventlog.Event
	for _, f := range c.frames {
		if f.Durable != nil {
			out = append(out, *f.Durable)
		}
	}
	return out
}

func (w *streamWorld) state(ctx context.Context) (string, error) {
	return w.log.State(w.tenantCtx(), w.runID)
}

func (w *streamWorld) completedMessage(user string) (map[string]any, error) {
	for _, e := range w.durable(user) {
		if e.Type != "message.completed" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(e.Data, &data); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("no message.completed event in %s's stream", user)
}

// deltaBus retains deltas per subject and validates the stream.Broker contract
// of platform.Bus without the JetStream transport; the scenarios read the
// frames the broker fans out in process.
type deltaBus struct {
	published []stream.Delta
}

func (b *deltaBus) Publish(ctx context.Context, subject string, data []byte) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, core.NewError(core.CodeCancelled, "cancelled", nil)
	}
	var d stream.Delta
	if err := json.Unmarshal(data, &d); err != nil {
		return 0, core.NewError(core.CodeInternal, "bad delta", nil)
	}
	b.published = append(b.published, d)
	return uint64(len(b.published)), nil
}

func (b *deltaBus) Subscribe(context.Context, string, uint64) (<-chan platform.Message, error) {
	return nil, core.NewError(core.CodeUnsupported, "deltas are fanned out in process", nil)
}

func (b *deltaBus) LastSeq(context.Context, string) (uint64, error) {
	return uint64(len(b.published)), nil
}

func (b *deltaBus) Purge(context.Context, string) error { return nil }

var errStreamNoWorld = errors.New("stream world not opened")

func joinText(chunks ...string) string { return strings.Join(chunks, "") }
