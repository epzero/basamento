package authz_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func TestCheckAllowedNoAudit(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	res := authz.Resource{Project: "p1", Kind: "run", ID: "r1"}
	if err := h.auth.Check(ctx, principal("ada", tenant), "run.get", res); err != nil {
		t.Fatalf("check: %v", err)
	}
	events, calls := h.audit.snap()
	if calls != 0 || len(events) != 0 {
		t.Fatalf("audit calls = %d events = %d, want 0", calls, len(events))
	}
}

func TestUndeclaredAction(t *testing.T) {
	h := newHarness(t)
	ctx := tenantCtx(t, "acme")
	err := h.auth.Check(ctx, principal("ada", "acme"), "bogus.action", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	events, _ := h.audit.snap()
	if len(events) != 1 || events[0].Reason != authz.ReasonUndeclaredAction {
		t.Fatalf("event %+v", events)
	}
}

func TestTenantMismatchAndMissing(t *testing.T) {
	h := newHarness(t)
	seedTenantWideAdmin(t, h, "acme", "ada")
	err := h.auth.Check(context.Background(), principal("ada", "acme"), "run.get", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	ctx := tenantCtx(t, "acme")
	err = h.auth.Check(ctx, principal("ada", "other"), "run.get", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	events, _ := h.audit.snap()
	if len(events) != 2 || events[0].Reason != authz.ReasonTenantMismatch || events[1].Reason != authz.ReasonTenantMismatch {
		t.Fatalf("events %+v", events)
	}
}

func TestDefaultDeny(t *testing.T) {
	h := newHarness(t)
	ctx := tenantCtx(t, "acme")
	mustDeny(t, h, ctx, principal("bob", "acme"), "run.get", authz.Resource{Kind: "run", ID: "r1"})
	events, _ := h.audit.snap()
	if events[0].Reason != authz.ReasonNoBinding {
		t.Fatalf("reason %q", events[0].Reason)
	}
}

func TestDenialMessageFixed(t *testing.T) {
	h := newHarness(t)
	ctx := tenantCtx(t, "acme")
	err := h.auth.Check(ctx, principal("bob", "acme"), "run.get", authz.Resource{Kind: "run", ID: "r1"})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Message != "permission denied" || ce.Details != nil {
		t.Fatalf("err %+v", err)
	}
	if strings.Contains(ce.Message, "run.get") {
		t.Fatal("leaked action")
	}
}

func TestAuditEventFields(t *testing.T) {
	h := newHarness(t)
	ctx := tenantCtx(t, "acme")
	res := authz.Resource{Project: "p1", Kind: "run", ID: "r1", Attrs: map[string]any{"secret": "x"}}
	mustDeny(t, h, ctx, principal("bob", "acme"), "run.get", res)
	events, _ := h.audit.snap()
	e := events[0]
	if e.Action != "authz.deny" || e.Outcome != "denied" || e.Principal != "bob" || e.Resource != "run:r1" {
		t.Fatalf("event %+v", e)
	}
	if e.Tenant != "" {
		t.Fatalf("tenant field must be empty for Record to derive from ctx")
	}
}

func TestUnionTwoBindings(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: ""})
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "bob", Role: authz.RoleMember, Project: "p1"})
	res := authz.Resource{Project: "p1", Kind: "run", ID: "r1"}
	if err := h.auth.Check(ctx, principal("bob", tenant), "run.start", res); err != nil {
		t.Fatal(err)
	}
}

func TestTenantWidePlusProjectBinding(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: ""})
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "bob", Role: authz.RoleMember, Project: "p1"})
	res := authz.Resource{Kind: "tenant", ID: "t"}
	if err := h.auth.Check(ctx, principal("bob", tenant), "pool.list", res); err != nil {
		t.Fatal(err)
	}
	mustDeny(t, h, ctx, principal("bob", tenant), "run.start", res)
}

func TestConditionTrueFalse(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "p1",
		Condition: `resource.id == "r1"`,
	})
	res := authz.Resource{Project: "p1", Kind: "run", ID: "r1"}
	if err := h.auth.Check(ctx, principal("bob", tenant), "run.get", res); err != nil {
		t.Fatal(err)
	}
	res.ID = "r2"
	mustDeny(t, h, ctx, principal("bob", tenant), "run.get", res)
	events, _ := h.audit.snap()
	if events[0].Reason != authz.ReasonNoBinding {
		t.Fatalf("reason %q", events[0].Reason)
	}
}

func TestConditionCostExceeded(t *testing.T) {
	sqlStore := openAuthzDB(t)
	audit := &memAudit{}
	clock := coretest.NewFakeClock(testStart)
	a, err := authz.New(authz.Options{
		Store:     authz.NewSQLStore(sqlStore),
		Audit:     audit,
		Clock:     clock,
		CostLimit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := authz.NewSQLStore(sqlStore)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	longList := make([]any, 500)
	for i := range longList {
		longList[i] = int64(1)
	}
	cond := `resource.attrs["nums"].all(n, n > 0)`
	mustStoreGrant(t, ctx, store, authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "",
		Condition: cond,
	})
	res := authz.Resource{
		Kind: "run", ID: "r1",
		Attrs: map[string]any{"nums": longList},
	}
	mustDeny(t, &harness{auth: a, audit: audit}, ctx, principal("bob", tenant), "run.get", res)
	events, n := audit.snap()
	if n != 1 {
		t.Fatalf("audit %d", n)
	}
	if events[0].Reason != authz.ReasonConditionCostExceed {
		t.Fatalf("reason %q", events[0].Reason)
	}
}

func TestConditionRuntimeError(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "",
		Condition: `resource.attrs.x > 1`,
	})
	res := authz.Resource{Kind: "run", ID: "r1", Attrs: map[string]any{"x": "not-a-number"}}
	mustDeny(t, h, ctx, principal("bob", tenant), "run.get", res)
	events, n := h.audit.snap()
	if n != 1 {
		t.Fatalf("audit %d", n)
	}
	if events[0].Reason != authz.ReasonConditionError {
		t.Fatalf("reason %q", events[0].Reason)
	}
}

func TestStoredUncompilableCondition(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: "x", Condition: `resource.`})
	mustDeny(t, h, ctx, principal("bob", tenant), "run.get", authz.Resource{Project: "x", Kind: "run", ID: "r1"})
	events, _ := h.audit.snap()
	if events[0].Reason != authz.ReasonConditionError {
		t.Fatalf("reason %q", events[0].Reason)
	}
}

func TestAuditRecordFailureStillDenies(t *testing.T) {
	h := newHarness(t)
	h.audit.err = core.NewError(core.CodeUnavailable, "store down", nil)
	ctx := tenantCtx(t, "acme")
	err := h.auth.Check(ctx, principal("bob", "acme"), "run.get", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	if h.metric.total != 1 {
		t.Fatalf("metric %d", h.metric.total)
	}
}

func TestScopeNarrowedWhenWouldAllow(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	p := principal("ada", tenant)
	p.Scopes = []string{"run:r1"}
	mustDeny(t, h, ctx, p, "run.get", authz.Resource{Kind: "run", ID: "r1"})
	events, _ := h.audit.snap()
	if events[0].Reason != authz.ReasonScopeNarrowed {
		t.Fatalf("reason %q", events[0].Reason)
	}
}

func TestScopeNarrowedNotWithoutBinding(t *testing.T) {
	h := newHarness(t)
	ctx := tenantCtx(t, "acme")
	p := principal("bob", "acme")
	p.Scopes = []string{"run:r1"}
	mustDeny(t, h, ctx, p, "run.get", authz.Resource{Kind: "run", ID: "r1"})
	events, _ := h.audit.snap()
	if events[0].Reason != authz.ReasonNoBinding {
		t.Fatalf("reason %q", events[0].Reason)
	}
}

func TestGrantValidationOrder(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "../p2",
	})
	mustCheckCode(t, err, core.CodeInvalidArgument)
	_, calls := h.audit.snap()
	if calls != 0 {
		t.Fatalf("audit calls %d", calls)
	}
	err = h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "p2", Condition: `resource.`,
	})
	mustCheckCode(t, err, core.CodePermissionDenied)
}

func TestGrantAuthorityMatrix(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	grantAdmin(t, h, tenant, "ada", "v", "", authz.RoleViewer)
	grantAdmin(t, h, tenant, "ada", "m", "", authz.RoleMember)
	err := h.auth.Grant(ctx, principal("v", tenant), authz.Binding{Principal: "x", Role: authz.RoleAdmin, Project: "p1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	err = h.auth.Grant(ctx, principal("m", tenant), authz.Binding{Principal: "x", Role: authz.RoleViewer, Project: "p1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	for _, role := range []authz.Role{authz.RoleViewer, authz.RoleMember, authz.RoleAdmin} {
		if err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{Principal: "t", Role: role, Project: "p1"}); err != nil {
			t.Fatalf("admin grant %s: %v", role, err)
		}
	}
	err = h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{Principal: "z", Role: authz.RoleViewer, Project: "p1"})
	if err != nil {
		t.Fatalf("project admin granting project ok: %v", err)
	}
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "proj-only", Role: authz.RoleAdmin, Project: "p9"})
	err = h.auth.Grant(ctx, principal("proj-only", tenant), authz.Binding{Principal: "w", Role: authz.RoleViewer, Project: ""})
	mustCheckCode(t, err, core.CodePermissionDenied)
}

func TestUnknownRoleMessage(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{Principal: "bob", Role: "super", Project: "p1"})
	mustCheckCode(t, err, core.CodeInvalidArgument)
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatal(err)
	}
	if strings.Contains(ce.Message, "super") || strings.Contains(ce.Message, "binding.grant") {
		t.Fatalf("leaked detail: %s", ce.Message)
	}
}

func TestGrammarBoundaries(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	ok64 := strings.Repeat("a", 64)
	bad65 := strings.Repeat("a", 65)
	for _, id := range []string{bad65, "-bad"} {
		err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: id})
		mustCheckCode(t, err, core.CodeInvalidArgument)
	}
	if err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: ok64}); err != nil {
		t.Fatalf("64 char project: %v", err)
	}
	ok128 := "A" + strings.Repeat("a", 127)
	bad129 := "A" + strings.Repeat("a", 128)
	if err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{Principal: bad129, Role: authz.RoleViewer, Project: "p1"}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("129 principal: %v", err)
	}
	if err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{Principal: ok128, Role: authz.RoleViewer, Project: "p1"}); err != nil {
		t.Fatalf("128 principal: %v", err)
	}
}
