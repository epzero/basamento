package eventlog

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestReadAfterCursorReturnsTheRestInOrder(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started", "node.started", "gate.opened", "run.completed")

	events, err := l.Read(tenantCtx("acme"), "r1", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 3 || events[0].Seq != 2 || events[2].Seq != 4 {
		t.Fatalf("events %+v, want sequences 2 to 4", events)
	}
	wantTypes(t, events, "node.started", "gate.opened", "run.completed")
}

func TestReadAtOrPastTheEndReturnsNothing(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started", "node.started", "run.completed")

	for _, after := range []uint64{3, 4, 99} {
		events, err := l.Read(tenantCtx("acme"), "r1", after)
		if err != nil {
			t.Fatalf("read after %d: %v", after, err)
		}
		if len(events) != 0 {
			t.Fatalf("read after %d returned %d events, want none", after, len(events))
		}
	}
}

func TestReadOfAnotherTenantsRunIsNotFound(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	appendTypes(t, l, "r1", "running", "run.started")

	if _, err := l.Read(tenantCtx("globex"), "r1", 0); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("code %s, want not_found", core.CodeOf(err))
	}
	if _, err := l.Outbox(tenantCtx("globex"), "r1"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("outbox code %s, want not_found", core.CodeOf(err))
	}
	if _, err := l.State(tenantCtx("globex"), "r1"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("state code %s, want not_found", core.CodeOf(err))
	}
}

func TestReadWithoutTenantIsInvalid(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	if _, err := l.Read(context.Background(), "r1", 0); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s, want invalid_argument", core.CodeOf(err))
	}
}

func TestReadDoesNotCrossTenants(t *testing.T) {
	l := testLog(t, nil)
	mustCreateRun(t, l, "r1", "running")
	mustCreateRun(t, l, "r2", "running")
	globexCtx := core.WithTenant(context.Background(), "globex")
	if err := l.CreateRun(globexCtx, "r2", "running"); err != nil {
		t.Fatalf("globex create: %v", err)
	}
	appendTypes(t, l, "r1", "running", "run.started")
	if _, err := l.AppendChange(globexCtx, "r2", "running", eventTypes("run.started")); err != nil {
		t.Fatalf("globex append: %v", err)
	}

	globex, err := l.Read(globexCtx, "r2", 0)
	if err != nil || len(globex) != 1 {
		t.Fatalf("globex read %+v, err %v", globex, err)
	}
	acme, err := l.Read(tenantCtx("acme"), "r2", 0)
	if err != nil || len(acme) != 0 {
		t.Fatalf("acme read of its own r2 returned %+v, err %v; want none", acme, err)
	}
	if _, err := l.Read(globexCtx, "r1", 0); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("globex read of acme r1: code %s, want not_found", core.CodeOf(err))
	}
}
