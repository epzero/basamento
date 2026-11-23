package authz_test

import (
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core/coretest"
)

func TestScopedSessionMatchConditionErrorReason(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{
		Principal: "w", Role: authz.RoleMember, Project: "p1",
		Condition: `resource.attrs.x > 1`,
	})
	p := principal("w", tenant)
	p.Scopes = []string{"session:s1"}
	res := runRes("s1")
	res.Attrs = map[string]any{"x": "not-a-number"}
	mustDeny(t, h, ctx, p, "run.cancel", res)
	events, n := h.audit.snap()
	if n != 1 || events[0].Reason != authz.ReasonConditionError {
		t.Fatalf("event %+v", events)
	}
}

func TestScopedSessionMatchConditionCostReason(t *testing.T) {
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	longList := make([]any, 500)
	for i := range longList {
		longList[i] = int64(1)
	}
	sqlStore := openAuthzDB(t)
	audit := &memAudit{}
	a, err := authz.New(authz.Options{
		Store:     authz.NewSQLStore(sqlStore),
		Audit:     audit,
		Clock:     coretest.NewFakeClock(testStart),
		CostLimit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := authz.NewSQLStore(sqlStore)
	mustStoreGrant(t, ctx, store, authz.Binding{
		Principal: "w", Role: authz.RoleMember, Project: "p1",
		Condition: `resource.attrs["nums"].all(n, n > 0)`,
	})
	p := principal("w", tenant)
	p.Scopes = []string{"session:s1"}
	res := runRes("s1")
	res.Attrs = map[string]any{"nums": longList}
	mustDeny(t, &harness{auth: a, audit: audit}, ctx, p, "run.cancel", res)
	events, n := audit.snap()
	if n != 1 || events[0].Reason != authz.ReasonConditionCostExceed {
		t.Fatalf("event %+v", events)
	}
}
