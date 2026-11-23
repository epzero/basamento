package authz

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func splitRoleTable() map[Role]PermissionSet {
	grantOnly := rolePerms(RoleAdmin).clone()
	delete(grantOnly, "binding.revoke")
	revokeOnly := rolePerms(RoleAdmin).clone()
	delete(revokeOnly, "binding.grant")
	out := cloneRoleTable(rolePermissions)
	out[Role("grant-only")] = grantOnly
	out[Role("revoke-only")] = revokeOnly
	return out
}

func TestGrantRevokeSeparateActions(t *testing.T) {
	sqlStore := openTestSQLStore(t)
	store := NewSQLStore(sqlStore)
	audit := &recordingAudit{}
	roles := splitRoleTable()
	roles[RoleAdmin] = rolePerms(RoleAdmin)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := store.Grant(ctx, Binding{Principal: "g", Role: Role("grant-only"), Project: ""}); err != nil {
		t.Fatal(err)
	}
	if err := store.Grant(ctx, Binding{Principal: "r", Role: Role("revoke-only"), Project: ""}); err != nil {
		t.Fatal(err)
	}
	a := &Authorizer{
		store:     store,
		audit:     audit,
		clock:     coretest.NewFakeClock(time.Unix(0, 0)),
		compile:   mustCompiler(t, defaultCostLimit),
		cache:     newConditionCache(8),
		costLimit: defaultCostLimit,
		roles:     roles,
	}
	p := authn.Principal{ID: "g", Tenant: "acme"}
	if err := a.Grant(ctx, p, Binding{Principal: "bob", Role: RoleViewer, Project: "p1"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	audit.calls = 0
	err := a.Revoke(ctx, p, "bob", RoleViewer, "p1")
	if core.CodeOf(err) != core.CodePermissionDenied {
		t.Fatalf("revoke: %v", err)
	}
	if audit.calls != 1 {
		t.Fatalf("audit %d", audit.calls)
	}
	p2 := authn.Principal{ID: "r", Tenant: "acme"}
	if err := a.Grant(ctx, p2, Binding{Principal: "carol", Role: RoleViewer, Project: "p2"}); core.CodeOf(err) != core.CodePermissionDenied {
		t.Fatalf("grant deny: %v", err)
	}
	if err := a.Revoke(ctx, p2, "bob", RoleViewer, "p1"); err != nil {
		t.Fatalf("revoke ok: %v", err)
	}
}
