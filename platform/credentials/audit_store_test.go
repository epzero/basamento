package credentials

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func recEvent(tenant core.TenantID, step string) platform.AuditEvent {
	return platform.AuditEvent{
		Time:      testTime,
		Tenant:    tenant,
		Principal: "human:ana",
		Action:    "credential.resolve",
		Resource:  "cred://vendor/anthropic",
		Outcome:   "allowed",
		RunID:     "r1",
		Step:      step,
		Reason:    "",
	}
}

func TestAuditRecordAndRead(t *testing.T) {
	db, _ := openTestDB(t)
	a := NewSQLAudit(db)
	if err := a.Record(tenantCtx("acme"), recEvent("acme", "s1")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := a.Record(tenantCtx("acme"), recEvent("", "s2")); err != nil {
		t.Fatalf("Record second: %v", err)
	}
	events, err := a.Read(tenantCtx("acme"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events %+v", events)
	}
	if events[0].Step != "s1" || events[1].Step != "s2" {
		t.Fatalf("order %+v", events)
	}
	want := recEvent("acme", "s1")
	if events[0] != want {
		t.Fatalf("event %+v want %+v", events[0], want)
	}
}

func TestAuditReadScopedToTenant(t *testing.T) {
	db, _ := openTestDB(t)
	a := NewSQLAudit(db)
	if err := a.Record(tenantCtx("acme"), recEvent("", "s1")); err != nil {
		t.Fatal(err)
	}
	if err := a.Record(tenantCtx("globex"), recEvent("", "s2")); err != nil {
		t.Fatal(err)
	}
	events, err := a.Read(tenantCtx("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Tenant != "acme" {
		t.Fatalf("events %+v", events)
	}
}

func TestAuditReadRequiresTenant(t *testing.T) {
	db, _ := openTestDB(t)
	a := NewSQLAudit(db)
	_, err := a.Read(context.Background())
	wantCode(t, err, core.CodeInvalidArgument)
}

func TestAuditRejectsMismatchedTenant(t *testing.T) {
	db, _ := openTestDB(t)
	a := NewSQLAudit(db)
	err := a.Record(tenantCtx("globex"), recEvent("acme", "s1"))
	wantCode(t, err, core.CodeInvalidArgument)
	err = a.Record(context.Background(), recEvent("acme", "s1"))
	wantCode(t, err, core.CodeInvalidArgument)
	for _, tenant := range []string{"acme", "globex"} {
		events, err := a.Read(tenantCtx(tenant))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 0 {
			t.Fatalf("%s events %+v", tenant, events)
		}
	}
}

func TestAuditDeploymentLevelEvent(t *testing.T) {
	db, _ := openTestDB(t)
	a := NewSQLAudit(db)
	event := platform.AuditEvent{
		Time:     testTime,
		Action:   "worker.refuse",
		Resource: "acme-gpu",
		Outcome:  "failed",
	}
	if err := a.Record(context.Background(), event); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events, err := a.Read(tenantCtx("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("tenant read returned deployment event: %+v", events)
	}
	var n int
	err = db.Execer().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_events WHERE tenant_id = ''`).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("deployment rows %d, %v", n, err)
	}
}

func TestAuditEventsNeverReplaced(t *testing.T) {
	db, _ := openTestDB(t)
	a := NewSQLAudit(db)
	for _, step := range []string{"s1", "s2", "s3"} {
		if err := a.Record(tenantCtx("acme"), recEvent("acme", step)); err != nil {
			t.Fatal(err)
		}
	}
	events, err := a.Read(tenantCtx("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events %+v", events)
	}
	if events[0].Step != "s1" || events[2].Step != "s3" {
		t.Fatalf("events %+v", events)
	}
}

func TestAuditReadMapsRowErrors(t *testing.T) {
	db, _ := openTestDB(t)
	a := NewSQLAudit(db)
	if err := a.Record(tenantCtx("acme"), recEvent("acme", "s1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Execer().Exec(context.Background(),
		`UPDATE audit_events SET at = ?`, "not-a-timestamp"); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	events, err := a.Read(tenantCtx("acme"))
	wantCode(t, err, core.CodeInternal)
	if events != nil {
		t.Fatalf("events %+v, want none", events)
	}
	assertNoDriverText(t, err)
}
