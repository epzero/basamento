package stream

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/stream/eventlog"
)

// blockingBus holds Publish until the test releases it, so a test can prove
// that the run's lock is free while the bus call is in flight.
type blockingBus struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingBus) Publish(ctx context.Context, _ string, _ []byte) (uint64, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return 1, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (b *blockingBus) Subscribe(context.Context, string, uint64) (<-chan platform.Message, error) {
	return nil, core.NewError(core.CodeUnsupported, "not used", nil)
}

func (b *blockingBus) LastSeq(context.Context, string) (uint64, error) { return 0, nil }
func (b *blockingBus) Purge(context.Context, string) error             { return nil }

func tenantSubject(t *testing.T, tenantID, runID string) string {
	t.Helper()
	got, err := DeltaSubject(core.TenantID(tenantID), runID)
	if err != nil {
		t.Fatalf("delta subject: %v", err)
	}
	return got
}

// F1: two tenants' run "r1" must not share one delta subject.
func TestDeltaSubjectsAreTenantScoped(t *testing.T) {
	broker, log, _, bus := testBroker(t)
	for _, id := range []core.TenantID{"acme", "globex"} {
		ctx := tenant(string(id))
		if err := log.CreateRun(ctx, "r1", "running"); err != nil {
			t.Fatalf("create %s/r1: %v", id, err)
		}
		if err := broker.Open(ctx, "r1", RunOptions{}); err != nil {
			t.Fatalf("open %s/r1: %v", id, err)
		}
		if _, err := broker.Publish(ctx, "r1", Delta{MessageID: "m1", Kind: "text", Text: "x"}); err != nil {
			t.Fatalf("publish %s/r1: %v", id, err)
		}
	}
	if len(bus.published) != 2 {
		t.Fatalf("bus got %d deltas, want 2", len(bus.published))
	}
	if bus.published[0].subject == bus.published[1].subject {
		t.Fatalf("two tenants share the delta subject %q", bus.published[0].subject)
	}
	if want := "run." + hex.EncodeToString([]byte("acme")) + ".r1.deltas"; bus.published[0].subject != want {
		t.Fatalf("acme subject %q, want %q", bus.published[0].subject, want)
	}
	if want := "run." + hex.EncodeToString([]byte("globex")) + ".r1.deltas"; bus.published[1].subject != want {
		t.Fatalf("globex subject %q, want %q", bus.published[1].subject, want)
	}
	if _, err := DeltaSubject("", "r1"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("empty tenant subject: %v, want invalid_argument", err)
	}
	if _, err := DeltaSubject("acme", "bad id"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("bad run id subject: %v, want invalid_argument", err)
	}
}

// F2: a stalled bus must not hold run.mu, or Cancel and Heartbeat miss the
// heartbeat interval (REQ-STRM-005).
func TestAStalledBusDoesNotBlockCancellationOrHeartbeat(t *testing.T) {
	bus := &blockingBus{entered: make(chan struct{}, 1), release: make(chan struct{})}
	broker, log, _ := newTestBroker(t, bus, nil)
	startRun(t, broker, log, "r1")
	ctx := tenant("acme")

	published := make(chan error, 1)
	go func() {
		_, err := broker.Publish(ctx, "r1", Delta{MessageID: "m1", Kind: "text", Text: "x"})
		published <- err
	}()
	guard, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelGuard()
	select {
	case <-bus.entered:
	case <-guard.Done():
		t.Fatal("publish never reached the bus")
	}

	heartbeat := make(chan error, 1)
	go func() { heartbeat <- broker.Heartbeat(ctx, "r1") }()
	select {
	case err := <-heartbeat:
		if err != nil {
			t.Fatalf("heartbeat while the bus is stalled: %v", err)
		}
	case <-guard.Done():
		t.Fatal("heartbeat waited for the stalled bus")
	}

	canceled := make(chan error, 1)
	go func() { canceled <- broker.Cancel(ctx, "r1") }()
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatalf("cancel while the bus is stalled: %v", err)
		}
	case <-guard.Done():
		t.Fatal("cancel waited for the stalled bus")
	}

	close(bus.release)
	if err := <-published; core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("publish after the cancellation: %v, want cancelled", err)
	}
	if state, err := log.State(ctx, "r1"); err != nil || state != "cancelled" {
		t.Fatalf("state %q err %v, want cancelled", state, err)
	}
}

// F3: an attachment that overflows is closed but must also leave the run's
// subscribers, so the detach policy can fire.
func TestAnAttachmentThatFallsBehindIsRemovedFromTheRun(t *testing.T) {
	broker, log, clock, _ := testBroker(t)
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{DetachAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("open: %v", err)
	}
	a, err := broker.Attach(ctx, "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = a.Close() }()

	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	run.mu.Lock()
	for i := 0; i <= maxBufferedFrames; i++ {
		delta := Delta{MessageID: "m1", Kind: "text", Text: "x", DSeq: int64(i + 1)}
		run.broadcast(Frame{Delta: &delta})
	}
	subs := len(run.subs)
	run.mu.Unlock()
	if subs != 0 {
		t.Fatalf("the run still has %d subscriber(s) after the overflow", subs)
	}

	clock.Advance(30 * time.Minute)
	canceled, err := broker.EnforceDetachPolicies(ctx)
	if err != nil || len(canceled) != 1 || canceled[0] != "r1" {
		t.Fatalf("at the deadline canceled %v err %v, want r1", canceled, err)
	}
}

// F4a: a replay larger than the attachment buffer fails instead of buffering
// the whole log.
func TestAttachRejectsAnOversizedReplay(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{}); err != nil {
		t.Fatalf("open: %v", err)
	}
	events := make([]eventlog.NewEvent, maxBufferedFrames+1)
	for i := range events {
		events[i] = eventlog.NewEvent{Type: "run.started"}
	}
	if _, err := broker.AppendChange(ctx, "r1", "running", events); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := broker.Attach(ctx, "r1", Cursor{}); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("attach with a %d-event replay: %v, want unavailable", len(events), err)
	}
}

// F4b: a structurally bad or ahead cursor fails instead of replaying silently.
func TestAttachRejectsABadCursor(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ctx := tenant("acme")

	bad := []Cursor{
		{Seq: -1},
		{Open: map[string]int64{"bad id!": 0}},
		{Open: map[string]int64{"m1": -1}},
		{Open: map[string]int64{"m1": 1}},
	}
	for _, cursor := range bad {
		if _, err := broker.Attach(ctx, "r1", cursor); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("attach with cursor %+v: %v, want invalid_argument", cursor, err)
		}
	}

	if _, err := broker.Publish(ctx, "r1", Delta{MessageID: "m1", Kind: "text", Text: "hi"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	good, err := broker.Attach(ctx, "r1", Cursor{Open: map[string]int64{"m1": 1}})
	if err != nil {
		t.Fatalf("attach at the current dseq: %v", err)
	}
	_ = good.Close()
	if _, err := broker.Attach(ctx, "r1", Cursor{Open: map[string]int64{"m1": 2}}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("attach past the message: %v, want invalid_argument", err)
	}
}

// F5: a run the log holds in a terminal state cannot be reopened, published
// to, or given a live subscriber, even when the in-memory flag is stale.
func TestATerminalRunStaysTerminal(t *testing.T) {
	for _, tc := range []struct {
		state string
		code  core.Code
	}{
		{"completed", core.CodeConflict},
		{"failed", core.CodeConflict},
		{"cancelled", core.CodeCancelled},
	} {
		t.Run(tc.state, func(t *testing.T) {
			broker, log, _, _ := testBroker(t)
			startRun(t, broker, log, "r1")
			ctx := tenant("acme")
			if _, err := log.AppendChange(ctx, "r1", tc.state, []eventlog.NewEvent{{Type: "run." + tc.state}}); err != nil {
				t.Fatalf("record %s: %v", tc.state, err)
			}

			a, err := broker.Attach(ctx, "r1", Cursor{})
			if err != nil {
				t.Fatalf("attach to a %s run: %v", tc.state, err)
			}
			defer func() { _ = a.Close() }()
			run, err := broker.live(ctx, "r1")
			if err != nil {
				t.Fatalf("live: %v", err)
			}
			run.mu.Lock()
			subs := len(run.subs)
			run.mu.Unlock()
			if subs != 0 {
				t.Fatalf("a %s run took %d live subscriber(s)", tc.state, subs)
			}

			if _, err := broker.Publish(ctx, "r1", Delta{MessageID: "m1", Kind: "text", Text: "late"}); core.CodeOf(err) != tc.code {
				t.Fatalf("publish to a %s run: %v, want %s", tc.state, err, tc.code)
			}
			if err := broker.Open(ctx, "r1", RunOptions{}); core.CodeOf(err) != core.CodeConflict {
				t.Fatalf("open a %s run: %v, want conflict", tc.state, err)
			}
			if err := broker.Finish(ctx, "r1"); err != nil {
				t.Fatalf("finish a %s run: %v", tc.state, err)
			}
			if err := broker.Open(ctx, "r1", RunOptions{}); core.CodeOf(err) != core.CodeConflict {
				t.Fatalf("open a finished %s run: %v, want conflict", tc.state, err)
			}
		})
	}
}

// F7a: a terminal state change ends the run's subscribers with ErrDetached and
// drops the text it kept.
func TestATerminalAppendChangeEndsItsSubscribers(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ctx := tenant("acme")
	a, err := broker.Attach(ctx, "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = a.Close() }()
	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if _, err := broker.AppendChange(ctx, "r1", "completed", []eventlog.NewEvent{{Type: "run.completed"}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	for {
		if _, ok := a.TryNext(); !ok {
			break
		}
	}
	if _, err := a.Next(ctx); !errors.Is(err, ErrDetached) {
		t.Fatalf("next after the run finished: %v, want ErrDetached", err)
	}
	run.mu.Lock()
	messages, subs := len(run.messages), len(run.subs)
	run.mu.Unlock()
	if messages != 0 || subs != 0 {
		t.Fatalf("finished run kept %d message(s) and %d subscriber(s)", messages, subs)
	}
}

// F7b: Finish forgets the run, purges its deltas and is safe to repeat.
func TestFinishForgetsTheRunAndPurgesItsDeltas(t *testing.T) {
	broker, log, _, bus := testBroker(t)
	startRun(t, broker, log, "r1")
	ctx := tenant("acme")
	if _, err := broker.AppendChange(ctx, "r1", "completed", []eventlog.NewEvent{{Type: "run.completed"}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := broker.Finish(ctx, "r1"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := broker.live(ctx, "r1"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("live after finish: %v, want not_found", err)
	}
	want := tenantSubject(t, "acme", "r1")
	if len(bus.purged) != 1 || bus.purged[0] != want {
		t.Fatalf("purged %v, want [%s]", bus.purged, want)
	}
	if err := broker.Finish(ctx, "r1"); err != nil {
		t.Fatalf("finish twice: %v", err)
	}
}

// F8: a run opened with a detach policy and never attached to is unattended
// from the start.
func TestADetachPolicyCancelsARunNeverAttached(t *testing.T) {
	broker, log, clock, _ := testBroker(t)
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{DetachAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("open: %v", err)
	}
	clock.Advance(30 * time.Minute)
	canceled, err := broker.EnforceDetachPolicies(ctx)
	if err != nil || len(canceled) != 1 || canceled[0] != "r1" {
		t.Fatalf("canceled %v err %v, want r1", canceled, err)
	}
	if state, err := log.State(ctx, "r1"); err != nil || state != "cancelled" {
		t.Fatalf("state %q err %v, want cancelled", state, err)
	}
}

// F9: a failed reset publish restores the text and attempt, not only dseq.
func TestAFailedResetRestoresTheMessage(t *testing.T) {
	broker, log, _, bus := testBroker(t)
	startRun(t, broker, log, "r1")
	ctx := tenant("acme")
	if _, err := broker.Reset(ctx, "r1", "m1", 2); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := broker.Publish(ctx, "r1", Delta{MessageID: "m1", Kind: "text", Text: "Hello", Attempt: 2}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	bus.down = true
	if _, err := broker.Reset(ctx, "r1", "m1", 3); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("reset with the bus down: %v, want unavailable", err)
	}
	bus.down = false

	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	run.mu.Lock()
	msg := *run.messages["m1"]
	run.mu.Unlock()
	if msg.text != "Hello" || msg.attempt != 2 || msg.dseq != 2 {
		t.Fatalf("after the failed reset: text %q attempt %d dseq %d, want Hello/2/2", msg.text, msg.attempt, msg.dseq)
	}
	next, err := broker.Publish(ctx, "r1", Delta{MessageID: "m1", Kind: "text", Text: "!"})
	if err != nil {
		t.Fatalf("publish after the failed reset: %v", err)
	}
	if next.DSeq != 3 || next.Attempt != 2 {
		t.Fatalf("delta after the failed reset: dseq %d attempt %d, want 3/2", next.DSeq, next.Attempt)
	}
	reset, err := broker.Reset(ctx, "r1", "m1", 3)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset.DSeq != 4 || reset.Attempt != 3 {
		t.Fatalf("reset: dseq %d attempt %d, want 4/3", reset.DSeq, reset.Attempt)
	}
}

// F6a: the durable append outlives a request whose context ends mid-write, so
// the run still reaches cancelled.
func TestCancelSettlesTheRunWhenTheRequestEndsMidAppend(t *testing.T) {
	ctx, cancel := context.WithCancel(tenant("acme"))
	var armed bool
	fault := func(point string) error {
		if armed && point == eventlog.PointBeforeAnyEvent {
			cancel()
		}
		return nil
	}
	broker, log, _ := newTestBroker(t, &fakeBus{}, fault)
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := broker.AppendChange(ctx, "r1", "running", []eventlog.NewEvent{{Type: "run.started"}}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	armed = true
	if err := broker.Cancel(ctx, "r1"); err != nil {
		t.Fatalf("cancel with a request that ends mid-append: %v", err)
	}
	if state, err := log.State(tenant("acme"), "r1"); err != nil || state != "cancelled" {
		t.Fatalf("state %q err %v, want cancelled", state, err)
	}
	done, err := broker.Canceled(ctx, "r1")
	if err != nil {
		t.Fatalf("canceled channel: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("cancellation was not signalled after the append")
	}
}

// F6b: when the append itself fails, the run keeps running and the producer is
// not told to stop.
func TestAFailedCancelAppendDoesNotSignalCancellation(t *testing.T) {
	ctx := tenant("acme")
	var armed bool
	fault := func(point string) error {
		if armed && point == eventlog.PointBeforeAnyEvent {
			return core.NewError(core.CodeInternal, "append failed", nil)
		}
		return nil
	}
	broker, log, _ := newTestBroker(t, &fakeBus{}, fault)
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := broker.AppendChange(ctx, "r1", "running", []eventlog.NewEvent{{Type: "run.started"}}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	armed = true
	if err := broker.Cancel(ctx, "r1"); core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("cancel with a failing append: %v, want internal", err)
	}
	if state, err := log.State(ctx, "r1"); err != nil || state != "running" {
		t.Fatalf("state %q err %v, want running", state, err)
	}
	done, err := broker.Canceled(ctx, "r1")
	if err != nil {
		t.Fatalf("canceled channel: %v", err)
	}
	select {
	case <-done:
		t.Fatal("cancellation was signalled before the durable append")
	default:
	}
	if err := broker.Heartbeat(ctx, "r1"); err != nil {
		t.Fatalf("heartbeat after a failed cancel: %v", err)
	}
}
