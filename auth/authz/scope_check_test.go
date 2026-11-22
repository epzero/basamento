package authz_test

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
)

func seedMemberP1(t *testing.T, h *harness, tenant string) {
	t.Helper()
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{
		Principal: "w", Role: authz.RoleMember, Project: "p1",
	})
}

func runRes(session string) authz.Resource {
	return authz.Resource{Project: "p1", Kind: "run", ID: "r1", Session: session}
}

func TestScopeCheckEndToEnd(t *testing.T) {
	cases := []struct {
		name    string
		seed    bool
		scopes  []string
		session string
		action  string
		allow   bool
		reason  string
	}{
		{"no scopes allow", true, nil, "s1", "run.cancel", true, ""},
		{"session match", true, []string{"session:s1"}, "s1", "run.cancel", true, ""},
		{"session mismatch", true, []string{"session:s1"}, "s2", "run.cancel", false, authz.ReasonScopeNarrowed},
		{"openid only", true, []string{"openid"}, "s1", "run.cancel", false, authz.ReasonScopeNarrowed},
		{"session no binding", false, []string{"session:s1"}, "s1", "run.cancel", false, authz.ReasonNoBinding},
		{"openid no binding", false, []string{"openid"}, "s1", "run.cancel", false, authz.ReasonScopeNarrowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := "acme"
			if tc.seed {
				seedMemberP1(t, h, tenant)
			}
			ctx := tenantCtx(t, tenant)
			p := principal("w", tenant)
			p.Scopes = tc.scopes
			res := runRes(tc.session)
			err := h.auth.Check(ctx, p, tc.action, res)
			if tc.allow {
				if err != nil {
					t.Fatalf("check: %v", err)
				}
				_, n := h.audit.snap()
				if n != 0 {
					t.Fatalf("audit %d", n)
				}
				return
			}
			mustCheckCode(t, err, core.CodePermissionDenied)
			events, n := h.audit.snap()
			if n != 1 {
				t.Fatalf("audit %d", n)
			}
			if events[0].Reason != tc.reason {
				t.Fatalf("reason %q", events[0].Reason)
			}
			if events[0].Resource != "run:r1" {
				t.Fatalf("resource leaked session: %q", events[0].Resource)
			}
			if tc.session != "" && strings.Contains(events[0].Resource, tc.session) {
				t.Fatalf("session id in audit resource %q", events[0].Resource)
			}
			if strings.Contains(events[0].Resource, "session:") {
				t.Fatalf("scope prefix in audit %q", events[0].Resource)
			}
		})
	}
}

func TestScopedPrincipalCannotGrant(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	p := principal("ada", tenant)
	p.Scopes = []string{"session:s1"}
	err := h.auth.Grant(ctx, p, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: "p1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	events, n := h.audit.snap()
	if n != 1 || events[0].Reason != authz.ReasonScopeNarrowed {
		t.Fatalf("event %+v", events)
	}
}

func TestTenantEmptyPrincipalMismatch(t *testing.T) {
	h := newHarness(t)
	err := h.auth.Check(context.Background(), authn.Principal{ID: "bob", Tenant: ""}, "run.get", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	events, _ := h.audit.snap()
	if len(events) != 1 || events[0].Reason != authz.ReasonTenantMismatch {
		t.Fatalf("events %+v", events)
	}
	ctx := tenantCtx(t, "acme")
	err = h.auth.Check(ctx, authn.Principal{ID: "bob", Tenant: ""}, "run.get", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
}
