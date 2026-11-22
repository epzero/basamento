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

func seedViewerP1(t *testing.T, h *harness, tenant string) {
	t.Helper()
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{
		Principal: "w", Role: authz.RoleViewer, Project: "p1",
	})
}

func runRes(session string) authz.Resource {
	return authz.Resource{Project: "p1", Kind: "run", ID: "r1", Session: session}
}

type scopeE2ECase struct {
	name         string
	seed         string // "", "member", "viewer"
	scopes       []string
	session      string
	action       string
	allow        bool
	reason       string
	wantCode     core.Code
	principal    string
	principalTen string
	ctxTenant    string
	undeclared   bool
}

func runScopeE2E(t *testing.T, tc scopeE2ECase, h *harness) {
	t.Helper()
	tenant := tc.ctxTenant
	if tenant == "" {
		tenant = "acme"
	}
	switch tc.seed {
	case "member":
		seedMemberP1(t, h, tenant)
	case "viewer":
		seedViewerP1(t, h, tenant)
	}
	ctx := tenantCtx(t, tenant)
	pid := tc.principal
	if pid == "" {
		pid = "w"
	}
	pt := tc.principalTen
	if pt == "" {
		pt = tenant
	}
	p := principal(pid, pt)
	p.Scopes = tc.scopes
	res := runRes(tc.session)
	action := tc.action
	if action == "" {
		action = "run.cancel"
	}
	var err error
	if tc.undeclared {
		err = h.auth.Check(ctx, p, "bogus.action", res)
	} else {
		err = h.auth.Check(ctx, p, action, res)
	}
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
	code := tc.wantCode
	if code == "" {
		code = core.CodePermissionDenied
	}
	mustCheckCode(t, err, code)
	if code != core.CodePermissionDenied {
		_, n := h.audit.snap()
		if n != 0 {
			t.Fatalf("audit %d want 0", n)
		}
		return
	}
	events, n := h.audit.snap()
	if n != 1 {
		t.Fatalf("audit %d", n)
	}
	if events[0].Reason != tc.reason {
		t.Fatalf("reason %q want %q", events[0].Reason, tc.reason)
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
}

func TestScopeCheckEndToEnd(t *testing.T) {
	cases := []scopeE2ECase{
		{name: "no scopes allow", seed: "member", allow: true},
		{name: "session match", seed: "member", scopes: []string{"session:s1"}, session: "s1", allow: true},
		{name: "session mismatch", seed: "member", scopes: []string{"session:s1"}, session: "s2", reason: authz.ReasonScopeNarrowed},
		{name: "empty session resource", seed: "member", scopes: []string{"session:s1"}, session: "", reason: authz.ReasonScopeNarrowed},
		{name: "union s2", seed: "member", scopes: []string{"session:s1", "session:s2"}, session: "s2", allow: true},
		{name: "openid side s1", seed: "member", scopes: []string{"session:s1", "openid"}, session: "s1", allow: true},
		{name: "openid side s3", seed: "member", scopes: []string{"session:s1", "openid"}, session: "s3", reason: authz.ReasonScopeNarrowed},
		{name: "openid only", seed: "member", scopes: []string{"openid"}, session: "s1", reason: authz.ReasonScopeNarrowed},
		{name: "Session wrong case", seed: "member", scopes: []string{"Session:s1"}, session: "s1", reason: authz.ReasonScopeNarrowed},
		{name: "session empty id", seed: "member", scopes: []string{"session:"}, session: "", reason: authz.ReasonScopeNarrowed},
		{name: "session no binding", scopes: []string{"session:s1"}, session: "s1", reason: authz.ReasonNoBinding},
		{name: "openid no binding", scopes: []string{"openid"}, session: "s1", reason: authz.ReasonScopeNarrowed},
		{name: "viewer cancel", seed: "viewer", scopes: []string{"session:s1"}, session: "s1", action: "run.cancel", reason: authz.ReasonNoBinding},
		{name: "viewer get", seed: "viewer", scopes: []string{"session:s1"}, session: "s1", action: "run.get", allow: true},
		{name: "tenant mismatch openid", seed: "member", scopes: []string{"openid"}, session: "s1", principalTen: "globex", reason: authz.ReasonTenantMismatch},
		{name: "undeclared action openid", seed: "member", scopes: []string{"openid"}, session: "s1", undeclared: true, reason: authz.ReasonUndeclaredAction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			runScopeE2E(t, tc, h)
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
	events, n := h.audit.snap()
	if n != 1 || events[0].Reason != authz.ReasonTenantMismatch {
		t.Fatalf("events %+v", events)
	}
	ctx := tenantCtx(t, "acme")
	err = h.auth.Check(ctx, authn.Principal{ID: "bob", Tenant: ""}, "run.get", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodePermissionDenied)
	events, n = h.audit.snap()
	if n != 2 || events[1].Reason != authz.ReasonTenantMismatch {
		t.Fatalf("events %+v count %d", events, n)
	}
}
