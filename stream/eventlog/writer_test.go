package eventlog

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
	busport "github.com/epzero/basamento/platform/bus"
)

func TestAppendNumbersEventsFromOne(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")

	got := appendTypes(t, l, "r1", "running", "node.started", "gate.opened")
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("appended %+v, want sequences 1 and 2", got)
	}
	wantTypes(t, readAll(t, l, "r1"), "node.started", "gate.opened")
	state, err := l.State(tenantCtx("acme"), "r1")
	if err != nil || state != "running" {
		t.Fatalf("state %q, err %v, want running", state, err)
	}
}

func TestAppendContinuesAfterExistingEvents(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started")

	got := appendTypes(t, l, "r1", "completed", "run.completed")
	if len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("appended %+v, want sequence 2", got)
	}
	wantTypes(t, readAll(t, l, "r1"), "run.started", "run.completed")
}

func TestConcurrentAppendsGetDistinctSequences(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	mustCreateRun(t, l, "r2", "running")

	const n = 20
	start := make(chan struct{})
	errs := make(chan error, 2*n)
	var wg sync.WaitGroup
	for _, runID := range []string{"r1", "r2"} {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(runID string) {
				defer wg.Done()
				<-start
				_, err := l.AppendChange(tenantCtx("acme"), runID, "running", eventTypes("node.started"))
				errs <- err
			}(runID)
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	for _, runID := range []string{"r1", "r2"} {
		events := readAll(t, l, runID)
		if len(events) != n {
			t.Fatalf("%s has %d events, want %d", runID, len(events), n)
		}
		for i, e := range events {
			if e.Seq != uint64(i+1) {
				t.Fatalf("%s event %d has seq %d, want %d", runID, i, e.Seq, i+1)
			}
		}
	}
}

func TestAppendRejectsUnknownRun(t *testing.T) {
	l := testLog(t, nil)
	_, err := l.AppendChange(tenantCtx("acme"), "missing", "running", eventTypes("node.started"))
	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("code %s, want not_found", core.CodeOf(err))
	}
	if _, err := l.State(tenantCtx("acme"), "missing"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("state code %s, want not_found", core.CodeOf(err))
	}
}

func TestAppendWithoutTenantFails(t *testing.T) {
	l := testLog(t, nil)
	if err := l.CreateRun(context.Background(), "r1", "running"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("create code %s, want invalid_argument", core.CodeOf(err))
	}
	if _, err := l.AppendChange(context.Background(), "r1", "running", nil); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("append code %s, want invalid_argument", core.CodeOf(err))
	}
}

func TestAppendRollsBackAtEveryPoint(t *testing.T) {
	points := []string{
		PointBeforeAnyEvent,
		firstEventWritten,
		secondEventWritten,
		PointStateChangeSaved,
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			l := testLog(t, faultAt(point, core.CodeInternal))
			mustCreateRun(t, l, "r1", "running")

			if _, err := l.AppendChange(tenantCtx("acme"), "r1", "completed", eventTypes("node.completed", "run.completed")); err == nil {
				t.Fatal("append succeeded, want a stopped write")
			}
			state, err := l.State(tenantCtx("acme"), "r1")
			if err != nil || state != "running" {
				t.Fatalf("state %q, err %v, want running", state, err)
			}
			if events := readAll(t, l, "r1"); len(events) != 0 {
				t.Fatalf("log holds %d events, want none", len(events))
			}
			outbox, err := l.Outbox(tenantCtx("acme"), "r1")
			if err != nil {
				t.Fatalf("outbox: %v", err)
			}
			if len(outbox) != 0 {
				t.Fatalf("outbox holds %d entries, want none", len(outbox))
			}
		})
	}
}

func TestRejectedWriteLeavesNoTrace(t *testing.T) {
	l := testLog(t, faultAt(firstEventWritten, core.CodeConflict))
	mustCreateRun(t, l, "r1", "running")

	_, err := l.AppendChange(tenantCtx("acme"), "r1", "completed", eventTypes("node.completed"))
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("code %s, want conflict", core.CodeOf(err))
	}
	state, err := l.State(tenantCtx("acme"), "r1")
	if err != nil || state != "running" {
		t.Fatalf("state %q, err %v, want running", state, err)
	}
	if events := readAll(t, l, "r1"); len(events) != 0 {
		t.Fatalf("log holds %d events, want none", len(events))
	}
}

func TestAppendRejectsMalformedEvent(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	bad := []struct {
		name  string
		event NewEvent
	}{
		{"empty type", NewEvent{}},
		{"non-json data", NewEvent{Type: "x", Data: json.RawMessage(`{`)}},
		{"negative version", NewEvent{Type: "x", V: -1}},
	}
	for _, tc := range bad {
		if _, err := l.AppendChange(tenantCtx("acme"), "r1", "running", []NewEvent{tc.event}); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%s: code %s, want invalid_argument", tc.name, core.CodeOf(err))
		}
	}
	if events := readAll(t, l, "r1"); len(events) != 0 {
		t.Fatalf("log holds %d events, want none", len(events))
	}
}

func TestAppendRejectsAnUnrelayableEnvelope(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started")

	// The data alone fits the bus limit; the envelope around it does not, so
	// the relay could never publish it and the run's outbox head would wedge.
	filler := strings.Repeat("x", busport.DefaultMaxMessageSize-40)
	big := NewEvent{Type: "node.delta", Data: json.RawMessage(`"` + filler + `"`)}
	_, err := l.AppendChange(tenantCtx("acme"), "r1", "completed", []NewEvent{big})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s, want invalid_argument: %v", core.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(busport.DefaultMaxMessageSize)) {
		t.Fatalf("error %q does not name the bus limit", err)
	}
	if events := readAll(t, l, "r1"); len(events) != 1 {
		t.Fatalf("log holds %d events, want only the accepted one", len(events))
	}
	if state, err := l.State(tenantCtx("acme"), "r1"); err != nil || state != "running" {
		t.Fatalf("state %q, err %v, want running", state, err)
	}

	// The run keeps accepting relayable events, and they reach the bus.
	if got := appendTypes(t, l, "r1", "completed", "run.completed"); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("appended %+v, want sequence 2", got)
	}
	fb := &fakeBus{}
	relay := newRelay(t, l, fb, nil)
	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("relay: %v", err)
	}
	wantSeqs(t, fb.sequences("r1"), 1, 2)
	if events := readAll(t, l, "r1"); len(events) != 2 {
		t.Fatalf("log holds %d events, want 2", len(events))
	}
}

func TestCreateRunTwiceIsConflict(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	if err := l.CreateRun(tenantCtx("acme"), "r1", "running"); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("code %s, want conflict", core.CodeOf(err))
	}
}

func TestRunSubject(t *testing.T) {
	got, err := RunSubject("acme", "r1")
	if err != nil || got != "run.61636d65.r1.events" {
		t.Fatalf("subject %q, err %v", got, err)
	}
	for _, bad := range []string{"", "r.1", "r 1", "r*", "r/1"} {
		if _, err := RunSubject("acme", bad); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("run id %q: code %s, want invalid_argument", bad, core.CodeOf(err))
		}
	}
	if _, err := RunSubject("", "r1"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("empty tenant: code %s, want invalid_argument", core.CodeOf(err))
	}
}
