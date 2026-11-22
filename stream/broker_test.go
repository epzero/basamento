package stream

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
	"github.com/epzero/basamento/stream/eventlog"
)

var brokerNow = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

// fakeBus records every published delta and keeps the bus contract's sequence
// numbering. Attachments are served in process by the broker, so the fake does
// not need subscriptions to prove the scenarios.
type fakeBus struct {
	published []publishedDelta
	purged    []string
	down      bool
}

type publishedDelta struct {
	subject string
	seq     uint64
	delta   Delta
}

func (b *fakeBus) Publish(_ context.Context, subject string, data []byte) (uint64, error) {
	if b.down {
		return 0, core.NewError(core.CodeUnavailable, "bus down", nil)
	}
	var d Delta
	if err := json.Unmarshal(data, &d); err != nil {
		return 0, core.NewError(core.CodeInternal, "bad delta", nil)
	}
	b.published = append(b.published, publishedDelta{subject: subject, seq: uint64(len(b.published) + 1), delta: d})
	return uint64(len(b.published)), nil
}

func (b *fakeBus) Subscribe(context.Context, string, uint64) (<-chan platform.Message, error) {
	return nil, core.NewError(core.CodeUnsupported, "not used", nil)
}

func (b *fakeBus) LastSeq(context.Context, string) (uint64, error) { return 0, nil }
func (b *fakeBus) Purge(_ context.Context, subject string) error {
	b.purged = append(b.purged, subject)
	return nil
}

func testBroker(t *testing.T) (*Broker, *eventlog.Log, *coretest.FakeClock, *fakeBus) {
	t.Helper()
	bus := &fakeBus{}
	broker, log, clock := newTestBroker(t, bus, nil)
	return broker, log, clock, bus
}

// newTestBroker builds a broker over a fresh SQLite log, with bus and an
// optional eventlog fault for the tests that stop the writer at a point.
func newTestBroker(t *testing.T, bus platform.Bus, fault eventlog.Fault) (*Broker, *eventlog.Log, *coretest.FakeClock) {
	t.Helper()
	dir := t.TempDir()
	store, err := psql.Open(context.Background(), "sqlite:///"+filepath.Join(dir, "kit.db"), eventlog.Migrations())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := coretest.NewFakeClock(brokerNow)
	log, err := eventlog.New(store, eventlog.Options{Clock: clock, Fault: fault})
	if err != nil {
		t.Fatalf("new log: %v", err)
	}
	broker, err := NewBroker(log, bus, BrokerOptions{Clock: clock})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	return broker, log, clock
}

func tenant(tenantID string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenantID))
}

func startRun(t *testing.T, broker *Broker, log *eventlog.Log, runID string) {
	t.Helper()
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, runID, "running"); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := broker.Open(ctx, runID, RunOptions{}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := broker.AppendChange(ctx, runID, "running", []eventlog.NewEvent{
		{Type: "run.started"},
		{Type: "message.started", Data: json.RawMessage(`{"message_id":"m1"}`)},
	}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if err := broker.StartMessage(ctx, runID, "m1"); err != nil {
		t.Fatalf("start message: %v", err)
	}
}

func deltas(t *testing.T, a *Attachment, want int) []Delta {
	t.Helper()
	var out []Delta
	for len(out) < want {
		frame, ok := a.TryNext()
		if !ok {
			t.Fatalf("attachment ended after %d deltas, want %d", len(out), want)
		}
		if frame.Delta != nil {
			out = append(out, *frame.Delta)
		}
	}
	return out
}

func densify(ds []Delta) []int64 {
	out := make([]int64, len(ds))
	for i, d := range ds {
		out[i] = d.DSeq
	}
	return out
}

func TestAttachReplaysDurableEventsAfterTheCursorExactlyOnce(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")

	a, err := broker.Attach(tenant("acme"), "r1", Cursor{Seq: 1})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = a.Close() }()

	frame, ok := a.TryNext()
	if !ok || frame.Durable == nil {
		t.Fatalf("got frame %+v, want durable event", frame)
	}
	if frame.Durable.Seq != 2 || frame.Durable.Type != "message.started" {
		t.Fatalf("replayed %+v, want sequence 2 message.started", frame.Durable)
	}
	if _, ok := a.TryNext(); ok {
		t.Fatal("replay after the cursor repeated or invented an event")
	}

	fresh, err := broker.Attach(tenant("acme"), "r1", Cursor{Seq: 99})
	if err != nil {
		t.Fatalf("attach past the end: %v", err)
	}
	defer func() { _ = fresh.Close() }()
	if _, ok := fresh.TryNext(); ok {
		t.Fatal("a cursor past the end replayed an event")
	}
}

func TestReattachSendsASnapshotThenLaterDeltasWithoutGapOrDuplicate(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")

	first, err := broker.Attach(tenant("acme"), "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach first: %v", err)
	}
	for _, text := range []string{"Hello ", "world, ", "how ", "are "} {
		if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: text}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if got := deltas(t, first, 4); len(got) != 4 {
		t.Fatalf("first viewer got %v", got)
	}
	cursor := first.Cursor()
	if cursor.Seq != 2 || cursor.Open["m1"] != 4 {
		t.Fatalf("cursor %+v, want seq 2 open m1 4", cursor)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("detach: %v", err)
	}

	second, err := broker.Attach(tenant("acme"), "r1", cursor)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	defer func() { _ = second.Close() }()
	snapshot, ok := second.TryNext()
	if !ok || snapshot.Snapshot == nil {
		t.Fatalf("first frame %+v, want a snapshot", snapshot)
	}
	if snapshot.Snapshot.MessageID != "m1" || snapshot.Snapshot.DSeq != 4 {
		t.Fatalf("snapshot %+v, want message m1 through dseq 4", snapshot.Snapshot)
	}
	if text := snapshot.Snapshot.Text; text != "Hello world, how are " {
		t.Fatalf("snapshot text %q", text)
	}
	for _, text := range []string{"you", "?"} {
		if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: text}); err != nil {
			t.Fatalf("publish live: %v", err)
		}
	}
	live := deltas(t, second, 2)
	if got := densify(live); got[0] != 5 || got[1] != 6 {
		t.Fatalf("live deltas %v, want 5, 6", got)
	}
	if live[0].Text+live[1].Text != "you?" {
		t.Fatalf("live text %q", live[0].Text+live[1].Text)
	}
}

func TestTwoViewersReceiveTheSameDurableOrder(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ana, err := broker.Attach(tenant("acme"), "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach ana: %v", err)
	}
	ben, err := broker.Attach(tenant("acme"), "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach ben: %v", err)
	}
	defer func() { _ = ana.Close(); _ = ben.Close() }()
	if _, err := broker.AppendChange(tenant("acme"), "r1", "completed", []eventlog.NewEvent{
		{Type: "message.completed", Data: json.RawMessage(`{"message_id":"m1","text":"hi","dseq":0}`)},
		{Type: "run.completed"},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	anaSeq := durableSeqs(ana)
	benSeq := durableSeqs(ben)
	if len(anaSeq) != 4 || len(benSeq) != 4 {
		t.Fatalf("durable frames ana %v ben %v", anaSeq, benSeq)
	}
	for i := range anaSeq {
		if anaSeq[i] != benSeq[i] {
			t.Fatalf("ana %v, ben %v", anaSeq, benSeq)
		}
	}
}

func durableSeqs(a *Attachment) []uint64 {
	var out []uint64
	for {
		frame, ok := a.TryNext()
		if !ok {
			return out
		}
		if frame.Durable != nil {
			out = append(out, frame.Durable.Seq)
		}
	}
}

func TestRetryResetsTheMessageBeforeTheNewAttemptDeltas(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ana, err := broker.Attach(tenant("acme"), "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = ana.Close() }()
	for _, text := range []string{"par", "tial", " text"} {
		if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: text, Attempt: 1}); err != nil {
			t.Fatalf("publish attempt 1: %v", err)
		}
	}
	reset, err := broker.Reset(tenant("acme"), "r1", "m1", 2)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset.Kind != "reset" || reset.Attempt != 2 || reset.DSeq != 4 {
		t.Fatalf("reset %+v, want kind reset attempt 2 dseq 4", reset)
	}
	for _, text := range []string{"Do", "ne."} {
		if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: text, Attempt: 2}); err != nil {
			t.Fatalf("publish attempt 2: %v", err)
		}
	}
	if err := broker.CompleteMessage(tenant("acme"), "r1", "m1", false); err != nil {
		t.Fatalf("complete: %v", err)
	}
	frames := drain(ana)
	resetAt, firstAttemptTwo := -1, -1
	for i, f := range frames {
		if f.Delta == nil {
			continue
		}
		if f.Delta.Kind == "reset" && resetAt < 0 {
			resetAt = i
		}
		if f.Delta.Kind == "text" && f.Delta.Attempt == 2 && firstAttemptTwo < 0 {
			firstAttemptTwo = i
		}
	}
	if resetAt < 0 || firstAttemptTwo < 0 || resetAt > firstAttemptTwo {
		t.Fatalf("reset at %d, first attempt-2 delta at %d, want reset first", resetAt, firstAttemptTwo)
	}
	completed := completedMessage(t, frames)
	if completed.Text != "Done." {
		t.Fatalf("completed text %q, want Done.", completed.Text)
	}
}

func drain(a *Attachment) []Frame {
	var out []Frame
	for {
		frame, ok := a.TryNext()
		if !ok {
			return out
		}
		out = append(out, frame)
	}
}

type completed struct {
	Text        string `json:"text"`
	DSeq        int64  `json:"dseq"`
	Interrupted bool   `json:"interrupted"`
	MessageID   string `json:"message_id"`
}

func completedMessage(t *testing.T, frames []Frame) completed {
	t.Helper()
	for _, f := range frames {
		if f.Durable == nil || f.Durable.Type != "message.completed" {
			continue
		}
		var c completed
		if err := json.Unmarshal(f.Durable.Data, &c); err != nil {
			t.Fatalf("message.completed data: %v", err)
		}
		return c
	}
	t.Fatal("no message.completed event")
	return completed{}
}

func TestCancelStopsTheProducerAndKeepsThePartialText(t *testing.T) {
	broker, log, clock, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ana, err := broker.Attach(tenant("acme"), "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = ana.Close() }()
	for _, text := range []string{"one ", "two ", "three"} {
		if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: text}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if err := broker.Cancel(tenant("acme"), "r1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// The producer learns of the cancellation on its next heartbeat, so a
	// whole heartbeat interval may pass before it stops; it never publishes a
	// later delta.
	clock.Advance(DefaultHeartbeat)
	if err := broker.Heartbeat(tenant("acme"), "r1"); core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("heartbeat after cancel: %v, want cancelled", err)
	}
	if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: "late"}); core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("publish after cancel: %v, want cancelled", err)
	}
	if state, err := log.State(tenant("acme"), "r1"); err != nil || state != "cancelled" {
		t.Fatalf("state %q err %v, want cancelled", state, err)
	}
	c := completedMessage(t, drain(ana))
	if !c.Interrupted || c.Text != "one two three" || c.DSeq != 3 {
		t.Fatalf("interrupted message %+v", c)
	}
}

func TestDetachPolicyCancelsAnUnattendedRunAfterTheDuration(t *testing.T) {
	broker, log, clock, _ := testBroker(t)
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{DetachAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("open: %v", err)
	}
	ana, err := broker.Attach(ctx, "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := ana.Close(); err != nil {
		t.Fatalf("detach: %v", err)
	}
	clock.Advance(30*time.Minute - time.Second)
	if canceled, err := broker.EnforceDetachPolicies(ctx); err != nil || len(canceled) != 0 {
		t.Fatalf("before the deadline canceled %v err %v", canceled, err)
	}
	clock.Advance(time.Second)
	canceled, err := broker.EnforceDetachPolicies(ctx)
	if err != nil || len(canceled) != 1 || canceled[0] != "r1" {
		t.Fatalf("at the deadline canceled %v err %v", canceled, err)
	}
	if state, err := log.State(ctx, "r1"); err != nil || state != "cancelled" {
		t.Fatalf("state %q err %v", state, err)
	}
}

func TestReattachBeforeTheDeadlineKeepsTheRunAlive(t *testing.T) {
	broker, log, clock, _ := testBroker(t)
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{DetachAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("open: %v", err)
	}
	ana, _ := broker.Attach(ctx, "r1", Cursor{})
	_ = ana.Close()
	clock.Advance(29 * time.Minute)
	again, err := broker.Attach(ctx, "r1", Cursor{})
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if canceled, err := broker.EnforceDetachPolicies(ctx); err != nil || len(canceled) != 0 {
		t.Fatalf("canceled %v err %v while attached", canceled, err)
	}
	if state, _ := log.State(ctx, "r1"); state != "running" {
		t.Fatalf("state %q, want running", state)
	}
	_ = again.Close()
}

func TestAttachIsTenantScoped(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")

	if _, err := broker.Attach(tenant("globex"), "r1", Cursor{}); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("attach from another tenant: %v, want not_found", err)
	}
	if _, err := broker.Attach(context.Background(), "r1", Cursor{}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("attach without tenant: %v, want invalid_argument", err)
	}
}

func TestTheRunCompletesWhileNobodyIsAttached(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ana, _ := broker.Attach(tenant("acme"), "r1", Cursor{})
	_ = ana.Close()

	if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: "Hello"}); err != nil {
		t.Fatalf("publish while detached: %v", err)
	}
	if err := broker.CompleteMessage(tenant("acme"), "r1", "m1", false); err != nil {
		t.Fatalf("complete message: %v", err)
	}
	if _, err := broker.AppendChange(tenant("acme"), "r1", "completed", []eventlog.NewEvent{{Type: "run.completed"}}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if state, err := log.State(tenant("acme"), "r1"); err != nil || state != "completed" {
		t.Fatalf("state %q err %v, want completed", state, err)
	}
	later, err := broker.Attach(tenant("acme"), "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach after completion: %v", err)
	}
	defer func() { _ = later.Close() }()
	frames := drain(later)
	if c := completedMessage(t, frames); c.Text != "Hello" || c.Interrupted {
		t.Fatalf("completed message %+v", c)
	}
}

func TestDeltaSequencesArePerMessage(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ctx := tenant("acme")
	if err := broker.StartMessage(ctx, "r1", "m2"); err != nil {
		t.Fatalf("start second message: %v", err)
	}
	a := Delta{MessageID: "m1", Kind: "text", Text: "a"}
	b := Delta{MessageID: "m2", Kind: "text", Text: "b"}
	published1, err := broker.Publish(ctx, "r1", a)
	if err != nil {
		t.Fatalf("publish m1: %v", err)
	}
	published2, err := broker.Publish(ctx, "r1", b)
	if err != nil {
		t.Fatalf("publish m2: %v", err)
	}
	again, err := broker.Publish(ctx, "r1", a)
	if err != nil {
		t.Fatalf("publish m1 again: %v", err)
	}
	if published1.DSeq != 1 || published2.DSeq != 1 || again.DSeq != 2 {
		t.Fatalf("dseq m1 %d m2 %d m1 again %d, want 1, 1, 2", published1.DSeq, published2.DSeq, again.DSeq)
	}
}

func TestAPublishedDeltaCarriesItsRunAndSubject(t *testing.T) {
	broker, log, _, bus := testBroker(t)
	startRun(t, broker, log, "r1")
	if _, err := broker.Publish(tenant("acme"), "r1", Delta{MessageID: "m1", Kind: "text", Text: "hi"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(bus.published) != 1 {
		t.Fatalf("bus got %d deltas, want 1", len(bus.published))
	}
	got := bus.published[0]
	if got.subject != "run.61636d65.r1.deltas" || got.delta.RunID != "r1" || got.delta.DSeq != 1 {
		t.Fatalf("published %+v", got)
	}
}
