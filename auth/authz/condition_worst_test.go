package authz_test

import (
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core/coretest"
)

func TestWorstConditionReasonEndToEnd(t *testing.T) {
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	longList := make([]any, 500)
	for i := range longList {
		longList[i] = int64(1)
	}
	res := authz.Resource{
		Kind: "run", ID: "r1",
		Attrs: map[string]any{"x": "bad", "nums": longList},
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
		Principal: "bob", Role: authz.RoleViewer, Project: "",
		Condition: `resource.attrs.x > 1`,
	})
	mustStoreGrant(t, ctx, store, authz.Binding{
		Principal: "bob", Role: authz.RoleMember, Project: "",
		Condition: `resource.attrs["nums"].all(n, n > 0)`,
	})
	mustDeny(t, &harness{auth: a, audit: audit}, ctx, principal("bob", tenant), "run.start", res)
	events, n := audit.snap()
	if n != 1 || events[0].Reason != authz.ReasonConditionCostExceed {
		t.Fatalf("event %+v", events)
	}
}
