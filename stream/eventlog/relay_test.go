package eventlog

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func TestRelayPublishesInSequenceOrderAndMarks(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started", "node.started", "run.completed")

	bus := &fakeBus{}
	relay := newRelay(t, l, bus, nil)
	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("relay: %v", err)
	}
	wantSeqs(t, bus.sequences("r1"), 1, 2, 3)
	assertRelayed(t, l, "r1", 3, 0)

	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("second relay: %v", err)
	}
	if got := bus.sequences("r1"); len(got) != 3 {
		t.Fatalf("second pass published %v, want nothing", got[3:])
	}
}

func TestRelayHoldsTheHeadWhenTheBusIsUnreachable(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started", "node.started", "run.completed")

	bus := &fakeBus{failAfter: 2}
	relay := newRelay(t, l, bus, nil)
	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("relay: %v", err)
	}
	wantSeqs(t, bus.sequences("r1"), 1, 2)
	assertRelayed(t, l, "r1", 2, 1)

	bus.setFailAfter(0)
	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("relay after recovery: %v", err)
	}
	wantSeqs(t, bus.sequences("r1"), 1, 2, 3)
	assertRelayed(t, l, "r1", 3, 0)
}

func TestRelayRepeatsAfterStoppingBeforeMarking(t *testing.T) {
	points := []string{
		"after sending entry 3, before marking",
		"while marking entry 3, before saving",
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			l := testLog(t, nil)
			mustCreateRun(t, l, "r1", "running")
			appendTypes(t, l, "r1", "running", "run.started", "node.started", "run.completed")

			bus := &fakeBus{}
			stopped := newRelay(t, l, bus, faultAt(point, core.CodeInternal))
			if err := stopped.Run(context.Background()); err != nil {
				t.Fatalf("stopped relay: %v", err)
			}
			wantSeqs(t, bus.sequences("r1"), 1, 2, 3)
			assertRelayed(t, l, "r1", 2, 1)

			restarted := newRelay(t, l, bus, nil)
			if err := restarted.Run(context.Background()); err != nil {
				t.Fatalf("restarted relay: %v", err)
			}
			wantSeqs(t, bus.sequences("r1"), 1, 2, 3, 3)
			assertRelayed(t, l, "r1", 3, 0)
		})
	}
}

func TestEnvelopeCarriesRunSequenceTypeVersionAndData(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	data := json.RawMessage(`{"node":"n1"}`)
	if _, err := l.AppendChange(tenantCtx("acme"), "r1", "running", []NewEvent{{Type: "run.started", Data: data}}); err != nil {
		t.Fatalf("append: %v", err)
	}

	bus := &fakeBus{}
	relay := newRelay(t, l, bus, nil)
	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("relay: %v", err)
	}
	if len(bus.envs) != 1 {
		t.Fatalf("published %d envelopes, want 1", len(bus.envs))
	}
	env := bus.envs[0]
	if env.RunID != "r1" || env.Seq != 1 || env.Type != "run.started" || env.V != DefaultSchemaVersion {
		t.Fatalf("envelope %+v", env)
	}
	if string(env.Data) != string(data) {
		t.Fatalf("data %s, want %s", env.Data, data)
	}
}

func TestDuplicateCopiesCarryTheSameIdentity(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started")

	bus := &fakeBus{}
	stopped := newRelay(t, l, bus, faultAt("after sending entry 1, before marking", core.CodeInternal))
	if err := stopped.Run(context.Background()); err != nil {
		t.Fatalf("stopped relay: %v", err)
	}
	restarted := newRelay(t, l, bus, nil)
	if err := restarted.Run(context.Background()); err != nil {
		t.Fatalf("restarted relay: %v", err)
	}
	if len(bus.envs) != 2 {
		t.Fatalf("published %d envelopes, want 2", len(bus.envs))
	}
	a, b := bus.envs[0], bus.envs[1]
	if a.RunID != b.RunID || a.Seq != b.Seq || a.Type != b.Type || a.V != b.V || string(a.Data) != string(b.Data) {
		t.Fatalf("copies differ: %+v and %+v", a, b)
	}
}

func TestRelayOnlyPublishesRunsOfEachTenant(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	globexCtx := core.WithTenant(context.Background(), "globex")
	if err := l.CreateRun(globexCtx, "r2", "running"); err != nil {
		t.Fatalf("globex create: %v", err)
	}
	appendTypes(t, l, "r1", "running", "run.started")
	if _, err := l.AppendChange(globexCtx, "r2", "running", eventTypes("run.started")); err != nil {
		t.Fatalf("globex append: %v", err)
	}

	bus := &fakeBus{}
	relay := newRelay(t, l, bus, nil)
	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("relay: %v", err)
	}
	wantSeqs(t, bus.sequences("r1"), 1)
	wantSeqs(t, bus.sequences("r2"), 1)
}

func TestRelayKeepsTenantsOnSeparateStreams(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	globexCtx := core.WithTenant(context.Background(), "globex")
	if err := l.CreateRun(globexCtx, "r1", "running"); err != nil {
		t.Fatalf("globex create: %v", err)
	}
	appendTypes(t, l, "r1", "running", "run.started", "node.started")
	if _, err := l.AppendChange(globexCtx, "r1", "running", eventTypes("run.started", "node.started")); err != nil {
		t.Fatalf("globex append: %v", err)
	}

	bus := &fakeBus{}
	relay := newRelay(t, l, bus, nil)
	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("relay: %v", err)
	}

	acmeSubject, err := RunSubject("acme", "r1")
	if err != nil {
		t.Fatalf("acme subject: %v", err)
	}
	globexSubject, err := RunSubject("globex", "r1")
	if err != nil {
		t.Fatalf("globex subject: %v", err)
	}
	if acmeSubject == globexSubject {
		t.Fatalf("tenants share the subject %q", acmeSubject)
	}
	wantSeqs(t, bus.sequencesOn(acmeSubject), 1, 2)
	wantSeqs(t, bus.sequencesOn(globexSubject), 1, 2)
}

func TestConcurrentRelayPassesPublishOneOrderedPass(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	const events = 50
	types := make([]string, events)
	for i := range types {
		types[i] = "node.started"
	}
	appendTypes(t, l, "r1", "running", types...)

	bus := &fakeBus{}
	relay := newRelay(t, l, bus, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- relay.Run(context.Background())
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("relay: %v", err)
		}
	}

	want := make([]uint64, events)
	for i := range want {
		want[i] = uint64(i + 1)
	}
	wantSeqs(t, bus.sequences("r1"), want...)
}

func TestRelayContainsAFailingRunAndPublishesTheRest(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	globexCtx := core.WithTenant(context.Background(), "globex")
	if err := l.CreateRun(globexCtx, "r1", "running"); err != nil {
		t.Fatalf("globex create: %v", err)
	}
	appendTypes(t, l, "r1", "running", "run.started")
	if _, err := l.AppendChange(globexCtx, "r1", "running", eventTypes("run.started")); err != nil {
		t.Fatalf("globex append: %v", err)
	}

	acmeSubject, err := RunSubject("acme", "r1")
	if err != nil {
		t.Fatalf("acme subject: %v", err)
	}
	globexSubject, err := RunSubject("globex", "r1")
	if err != nil {
		t.Fatalf("globex subject: %v", err)
	}
	bus := &fakeBus{failSubjects: map[string]error{
		acmeSubject: core.NewError(core.CodeInternal, "acme stream rejected", nil),
	}}
	relay := newRelay(t, l, bus, nil)
	if err := relay.Run(context.Background()); err == nil {
		t.Fatal("relay hid the failing run's error")
	}
	wantSeqs(t, bus.sequencesOn(acmeSubject))
	wantSeqs(t, bus.sequencesOn(globexSubject), 1)
}

func TestLoopRetriesAPassThatFailsOneRun(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	globexCtx := core.WithTenant(context.Background(), "globex")
	if err := l.CreateRun(globexCtx, "r1", "running"); err != nil {
		t.Fatalf("globex create: %v", err)
	}
	appendTypes(t, l, "r1", "running", "run.started")
	if _, err := l.AppendChange(globexCtx, "r1", "running", eventTypes("run.started")); err != nil {
		t.Fatalf("globex append: %v", err)
	}

	acmeSubject, err := RunSubject("acme", "r1")
	if err != nil {
		t.Fatalf("acme subject: %v", err)
	}
	globexSubject, err := RunSubject("globex", "r1")
	if err != nil {
		t.Fatalf("globex subject: %v", err)
	}
	bus := &fakeBus{
		failOnce:   map[string]error{acmeSubject: core.NewError(core.CodeInternal, "acme stream rejected", nil)},
		acceptedCh: make(chan string, 8),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	relay, err := NewRelay(l, bus, RelayOptions{
		Clock: coretest.NewFakeClock(testNow),
		After: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time)
			close(ch)
			return ch
		},
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- relay.Loop(ctx) }()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case subject := <-bus.acceptedCh:
			if subject == acmeSubject || subject == globexSubject {
				seen[subject] = true
			}
		case err := <-done:
			t.Fatalf("loop returned before publishing both runs: %v", err)
		case <-ctx.Done():
			t.Fatalf("timed out after publishing %v", seen)
		}
	}
	cancel()
	stop, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("loop: %v", err)
		}
	case <-stop.Done():
		t.Fatal("loop did not stop after cancellation")
	}
}

func TestLoopStopsWhenContextIsCancelled(t *testing.T) {
	l := testLog(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	relay := newRelay(t, l, &fakeBus{}, nil)
	if err := relay.Loop(ctx); err != nil {
		t.Fatalf("loop: %v", err)
	}
}

func assertRelayed(t *testing.T, l *Log, runID string, relayed, pending uint64) {
	t.Helper()
	entries, err := l.Outbox(tenantCtx("acme"), runID)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	var gotRelayed, gotPending uint64
	for _, e := range entries {
		if e.Relayed {
			gotRelayed++
		} else {
			gotPending++
		}
	}
	if gotRelayed != relayed || gotPending != pending {
		t.Fatalf("outbox has %d relayed and %d pending, want %d and %d", gotRelayed, gotPending, relayed, pending)
	}
}
