package authz_test

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
)

func TestGrantConditionRejectsClockRandom(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	for _, cond := range []string{`now() > 0`, `random() > 0.5`, `rand() > 1`, `now () > 0`} {
		err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{
			Principal: "bob", Role: authz.RoleViewer, Project: "p1", Condition: cond,
		})
		mustCheckCode(t, err, core.CodeInvalidArgument)
	}
	list, err := h.store.List(ctx, "bob", "p1")
	if err != nil || len(list) != 0 {
		t.Fatalf("stored bindings %v", list)
	}
}

func TestGrantConditionAcceptsLiteralsAndTimestamp(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	for _, cond := range []string{
		`resource.attrs.note == "now(x)"`,
		`timestamp("2026-01-01T00:00:00Z") < timestamp("2027-01-01T00:00:00Z")`,
	} {
		if err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{
			Principal: "bob", Role: authz.RoleViewer, Project: "p1", Condition: cond,
		}); err != nil {
			t.Fatalf("cond %q: %v", cond, err)
		}
	}
}

func TestGrantConditionLengthBound(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	padding2044 := strings.Repeat(" ", 2044)
	okCond := "true" + padding2044
	if len(okCond) != 2048 {
		t.Fatalf("okCond len %d", len(okCond))
	}
	err := h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "p1", Condition: okCond,
	})
	if err != nil {
		t.Fatalf("2048: %v", err)
	}
	badCond := okCond + " "
	if len(badCond) != 2049 {
		t.Fatalf("badCond len %d", len(badCond))
	}
	err = h.auth.Grant(ctx, principal("ada", tenant), authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "p2", Condition: badCond,
	})
	mustCheckCode(t, err, core.CodeInvalidArgument)
}

func TestConditionCostVsMisclassify(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{
		Principal: "bob", Role: authz.RoleViewer, Project: "",
		Condition: `resource.attrs.s == "cost limit 10000 exceeded" || resource.attrs.s.size() > 0`,
	})
	mustDeny(t, h, ctx, principal("bob", tenant), "run.get", authz.Resource{
		Kind: "run", ID: "r1", Attrs: map[string]any{"s": 1},
	})
	events, n := h.audit.snap()
	if n != 1 || events[0].Reason != authz.ReasonConditionError {
		t.Fatalf("event %+v", events)
	}
}
