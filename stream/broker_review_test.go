package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/stream/eventlog"
)

// EP114-F1 / STUB-OPEN: an attach that rehydrates a run from the log leaves a
// placeholder liveRun behind. Open must upgrade that placeholder instead of
// conflicting, or the run's detach policy is silently disabled (REQ-STRM-006).
func TestOpenUpgradesARehydratedRun(t *testing.T) {
	broker, log, clock, _ := testBroker(t)
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The run exists in the log but not in this process: the first attach
	// rehydrates it, as it does after a restart.
	if _, err := log.AppendChange(ctx, "r1", "running", []eventlog.NewEvent{
		{Type: "run.started"},
		{Type: "message.started", Data: []byte(`{"message_id":"m1"}`)},
	}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	a, err := broker.Attach(ctx, "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach before open: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{DetachAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("open after a rehydrating attach: %v", err)
	}
	if err := broker.Open(ctx, "r1", RunOptions{}); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("open a run that is already open: %v, want conflict", err)
	}
	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	run.mu.Lock()
	_, learned := run.messages["m1"]
	run.mu.Unlock()
	if !learned {
		t.Fatal("opening the run dropped the message the attach had learned")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("detach: %v", err)
	}
	clock.Advance(30 * time.Minute)
	canceled, err := broker.EnforceDetachPolicies(ctx)
	if err != nil || len(canceled) != 1 || canceled[0] != "r1" {
		t.Fatalf("at the deadline canceled %v err %v, want r1", canceled, err)
	}
	if state, err := log.State(ctx, "r1"); err != nil || state != "cancelled" {
		t.Fatalf("state %q err %v, want cancelled", state, err)
	}
}

// EP114-F2 / NIL-CLOCK: a rehydrated run must carry the broker's clock, or the
// overflow path panics when a slow client is detached (REQ-STRM-006).
func TestARehydratedRunUsesTheBrokerClock(t *testing.T) {
	broker, log, clock, _ := testBroker(t)
	ctx := tenant("acme")
	if err := log.CreateRun(ctx, "r1", "running"); err != nil {
		t.Fatalf("create: %v", err)
	}
	a, err := broker.Attach(ctx, "r1", Cursor{})
	if err != nil {
		t.Fatalf("attach without open: %v", err)
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
	subs, detachedAt := len(run.subs), run.detachedAt
	run.mu.Unlock()
	if subs != 0 {
		t.Fatalf("the run still has %d subscriber(s) after the overflow", subs)
	}
	if !detachedAt.Equal(clock.Now()) {
		t.Fatalf("detached at %v, want the broker clock's %v", detachedAt, clock.Now())
	}
}

// EP114-F3: a run the log already holds terminal must end its stale
// subscribers and release the cancellation signal, not leave clients blocked
// in Next after they drain what was buffered.
func TestAnExternalTerminalStateEndsStaleSubscribers(t *testing.T) {
	for _, tc := range []struct {
		state string
		code  core.Code
	}{
		{"completed", core.CodeConflict},
		{"cancelled", core.CodeCancelled},
	} {
		t.Run(tc.state, func(t *testing.T) {
			broker, log, _, _ := testBroker(t)
			startRun(t, broker, log, "r1")
			ctx := tenant("acme")
			a, err := broker.Attach(ctx, "r1", Cursor{Seq: 2})
			if err != nil {
				t.Fatalf("attach: %v", err)
			}
			defer func() { _ = a.Close() }()
			// Another writer moves the run on; this process does not know yet.
			if _, err := log.AppendChange(ctx, "r1", tc.state, []eventlog.NewEvent{{Type: "run." + tc.state}}); err != nil {
				t.Fatalf("record %s: %v", tc.state, err)
			}
			if err := broker.Heartbeat(ctx, "r1"); core.CodeOf(err) != tc.code {
				t.Fatalf("heartbeat after the external %s: %v, want %s", tc.state, err, tc.code)
			}
			guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := a.Next(guard); !errors.Is(err, ErrDetached) {
				t.Fatalf("next after the external %s: %v, want ErrDetached", tc.state, err)
			}
			if tc.state == "cancelled" {
				done, err := broker.Canceled(ctx, "r1")
				if err != nil {
					t.Fatalf("canceled channel: %v", err)
				}
				select {
				case <-done:
				default:
					t.Fatal("the cancellation signal was not closed")
				}
			}
		})
	}
}

// EP114-F4 / R1-01: the replay read must happen outside the run's lock, or a
// Cancel or Heartbeat waits for it and misses the heartbeat interval
// (REQ-STRM-005). Holding the lock here stands in for Cancel's acquisition: an
// attach that reads under the lock can never return, not even its own size
// error, while this test holds it.
func TestAttachReadsTheReplayOutsideTheRunLock(t *testing.T) {
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
	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}

	run.mu.Lock()
	defer run.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := broker.Attach(ctx, "r1", Cursor{})
		done <- err
	}()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case err := <-done:
		if core.CodeOf(err) != core.CodeUnavailable {
			t.Fatalf("attach over an oversized replay: %v, want unavailable", err)
		}
	case <-guard.Done():
		t.Fatal("attach held the run lock while reading the replay")
	}
}

// R1-02: a cursor that claims deltas for a message the run never saw would hide
// every delta up to the claim; reject it.
func TestAttachRejectsACursorForAMessageItNeverSaw(t *testing.T) {
	broker, log, _, _ := testBroker(t)
	startRun(t, broker, log, "r1")
	ctx := tenant("acme")

	if _, err := broker.Attach(ctx, "r1", Cursor{Open: map[string]int64{"ghost": 5}}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("attach with a ghost cursor: %v, want invalid_argument", err)
	}
	// A finished run has dropped its in-flight messages, but cannot produce a
	// delta the claim would hide: its own cursor is still accepted (STRM-002).
	if _, err := broker.AppendChange(ctx, "r1", "completed", []eventlog.NewEvent{{Type: "run.completed"}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	again, err := broker.Attach(ctx, "r1", Cursor{Seq: 3, Open: map[string]int64{"m1": 1}})
	if err != nil {
		t.Fatalf("reattach a finished run with its cursor: %v", err)
	}
	_ = again.Close()
}

// DETACH-RACE: a client that reattaches after the policy scan but before the
// cancellation must keep the run; the deadline is re-checked under the same
// lock that registers an attachment (REQ-STRM-006).
func TestADetachDeadlineRecheckSparesAReattachedRun(t *testing.T) {
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
	clock.Advance(30 * time.Minute)

	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	run.mu.Lock()
	expired := run.expiredLocked(clock.Now())
	run.mu.Unlock()
	if !expired {
		t.Fatal("the scan did not see the run expired")
	}
	// The reattach lands in the gap between the scan and the cancellation.
	again, err := broker.Attach(ctx, "r1", Cursor{})
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	defer func() { _ = again.Close() }()
	canceled, err := broker.cancelIfExpired(ctx, run, clock.Now())
	if err != nil {
		t.Fatalf("cancel expired: %v", err)
	}
	if canceled {
		t.Fatal("cancelled a run a client reattached to")
	}
	if state, err := log.State(ctx, "r1"); err != nil || state != "running" {
		t.Fatalf("state %q err %v, want running", state, err)
	}
	if canceled, err := broker.EnforceDetachPolicies(ctx); err != nil || len(canceled) != 0 {
		t.Fatalf("policy pass canceled %v err %v while a client is attached", canceled, err)
	}
}

// attachGate pauses the first read of an attach at point until resume, so a
// test can put an external append in the attach's unlocked window without
// timing or sleeps.
type attachGate struct {
	point   string
	entered chan struct{}
	release chan struct{}
	armed   bool
	blocked bool
}

func newAttachGate(point string) *attachGate {
	return &attachGate{point: point, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *attachGate) arm() { g.armed = true }

func (g *attachGate) resume() { close(g.release) }

func (g *attachGate) fault(point string) error {
	if !g.armed || point != g.point {
		return nil
	}
	if !g.blocked {
		g.blocked = true
		close(g.entered)
		<-g.release
	}
	return nil
}

func nextFrame(t *testing.T, a *Attachment) (Frame, error) {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.Next(guard)
}

func assertDurableThenDetached(t *testing.T, a *Attachment, seqs ...int64) {
	t.Helper()
	for _, want := range seqs {
		frame, err := nextFrame(t, a)
		if err != nil {
			t.Fatalf("next durable %d: %v", want, err)
		}
		if frame.Durable == nil || int64(frame.Durable.Seq) != want {
			t.Fatalf("frame %+v, want durable seq %d", frame, want)
		}
	}
	if _, err := nextFrame(t, a); !errors.Is(err, ErrDetached) {
		t.Fatalf("next after the replay: %v, want ErrDetached", err)
	}
}

// EP114-R2-01 / ATTACH-TERMINAL-STALE: the state read before the replay is
// stale once another writer finishes the run in the log. Attach must refresh
// under run.mu and settle the run, or it registers on a run that is already
// over: Next never returns and the terminal event is never delivered
// (REQ-STRM-002).
func TestAttachSettlesARunFinishedBeforeItTakesTheLock(t *testing.T) {
	ctx := tenant("acme")
	gate := newAttachGate(eventlog.PointStateRead)
	broker, log, _ := newTestBroker(t, &fakeBus{}, gate.fault)
	startRun(t, broker, log, "r1")
	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}

	// Hold the run's lock across the attach's unlocked reads, so the terminal
	// append lands in the gap before the attachment's registration.
	run.mu.Lock()
	gate.arm()
	var a *Attachment
	var attachErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		a, attachErr = broker.Attach(ctx, "r1", Cursor{Seq: 1})
	}()
	<-gate.entered
	// Another writer finishes the run in the log; this liveRun knows nothing
	// about it.
	if _, err := log.AppendChange(ctx, "r1", "completed", []eventlog.NewEvent{{Type: "run.completed"}}); err != nil {
		t.Fatalf("record completion: %v", err)
	}
	gate.resume()
	run.mu.Unlock()
	<-done
	if attachErr != nil {
		t.Fatalf("attach: %v", attachErr)
	}
	if a == nil {
		t.Fatal("attach returned no attachment")
	}
	assertDurableThenDetached(t, a, 2, 3)
}

// EP114-R2-01 / ATTACH-TERMINAL-STALE: an external append made while the
// unlocked replay is running is missing from both the replay and the stale
// state. Attach must extend the replay to the durable head under the lock and
// settle the run from the fresh state (REQ-STRM-002).
func TestAttachReplaysATerminalAppendMadeDuringTheUnlockedReplay(t *testing.T) {
	ctx := tenant("acme")
	gate := newAttachGate(eventlog.PointEventsRead)
	broker, log, _ := newTestBroker(t, &fakeBus{}, gate.fault)
	startRun(t, broker, log, "r1")
	run, err := broker.live(ctx, "r1")
	if err != nil {
		t.Fatalf("live: %v", err)
	}

	run.mu.Lock()
	gate.arm()
	var a *Attachment
	var attachErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		a, attachErr = broker.Attach(ctx, "r1", Cursor{Seq: 1})
	}()
	<-gate.entered
	// The replay returned sequence 2 and paused; another writer appends the
	// run's end while the attach still holds no lock and no subscription.
	if _, err := log.AppendChange(ctx, "r1", "completed", []eventlog.NewEvent{{Type: "run.completed"}}); err != nil {
		t.Fatalf("record completion: %v", err)
	}
	gate.resume()
	run.mu.Unlock()
	<-done
	if attachErr != nil {
		t.Fatalf("attach: %v", attachErr)
	}
	if a == nil {
		t.Fatal("attach returned no attachment")
	}
	assertDurableThenDetached(t, a, 2, 3)
}
