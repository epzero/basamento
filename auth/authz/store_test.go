package authz_test

import (
	"testing"

	"github.com/epzero/basamento/auth/authz"
)

func TestStoreGrantRegrantRevokeList(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	b := authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: "p1", Condition: `true`}
	if err := h.store.Grant(ctx, b); err != nil {
		t.Fatal(err)
	}
	b.Condition = `resource.id == "r1"`
	if err := h.store.Grant(ctx, b); err != nil {
		t.Fatal(err)
	}
	list, err := h.store.List(ctx, "bob", "p1")
	if err != nil || len(list) != 1 || list[0].Condition != `resource.id == "r1"` {
		t.Fatalf("list %+v err %v", list, err)
	}
	if err := h.store.Revoke(ctx, "bob", authz.RoleViewer, "p1"); err != nil {
		t.Fatal(err)
	}
	list, err = h.store.List(ctx, "bob", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("want empty, got %+v", list)
	}
}

func TestStoreTenantIsolation(t *testing.T) {
	sqlStore := openAuthzDB(t)
	s := authz.NewSQLStore(sqlStore)
	ctxA := tenantCtx(t, "tenant-a")
	ctxB := tenantCtx(t, "tenant-b")
	mustStoreGrant(t, ctxA, s, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: "p1"})
	list, err := s.List(ctxB, "bob", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatal("cross-tenant read")
	}
	rows, err := s.BindingsForCheck(ctxA, "bob", "p1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("tenant-a rows %+v %v", rows, err)
	}
}

func TestStoreQueryProjectFilter(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: ""})
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: "bob", Role: authz.RoleMember, Project: "p1"})
	wide, err := h.store.BindingsForCheck(ctx, "bob", "")
	if err != nil || len(wide) != 1 {
		t.Fatalf("tenant-wide %+v", wide)
	}
	both, err := h.store.BindingsForCheck(ctx, "bob", "p1")
	if err != nil || len(both) != 2 {
		t.Fatalf("project %+v", both)
	}
}

func TestSQLiteMigrationsApply(t *testing.T) {
	openAuthzDB(t)
}
