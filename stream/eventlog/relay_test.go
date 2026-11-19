package eventlog

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
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
