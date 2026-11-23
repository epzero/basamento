package authz_test

import (
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
)

func TestListAuthority(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	_, err := h.auth.List(ctx, principal("nobody", tenant), "bob", "p1")
	mustCheckCode(t, err, core.CodePermissionDenied)
	_, n := h.audit.snap()
	if n != 1 {
		t.Fatalf("audit %d", n)
	}
	seedTenantWideAdmin(t, h, tenant, "ada")
	grantAdmin(t, h, tenant, "ada", "v", "", authz.RoleViewer)
	_, err = h.auth.List(ctx, principal("v", tenant), "bob", "p1")
	if err != nil {
		t.Fatalf("viewer list: %v", err)
	}
	p := principal("ada", tenant)
	p.Scopes = []string{"session:s1"}
	_, err = h.auth.List(ctx, p, "bob", "p1")
	mustCheckCode(t, err, core.CodePermissionDenied)
}
