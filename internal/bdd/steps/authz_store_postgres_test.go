package steps

import (
	"context"
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/sqltest"
	psql "github.com/epzero/basamento/platform/sql"
)

func openPostgresAuthzStore(t *testing.T) (*authz.SQLStore, func()) {
	t.Helper()
	ctx := context.Background()
	sqltest.ResetSkipCounts()
	sqltest.ResetPostgresForTest()
	t.Cleanup(func() { _ = sqltest.ClosePostgres() })
	if err := sqltest.InitPostgres(ctx); err != nil {
		if sqltest.GateStrict() {
			t.Fatal(err)
		}
	}
	if !sqltest.PostgresAvailable() {
		sqltest.RecordSkipRun(sqltest.SkipPostgresUnavailable)
		t.Skip("postgres-unavailable")
	}
	url, cleanup, err := sqltest.CreateDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mig := authz.Migrations()
	s, err := psql.Open(ctx, url, mig)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	return authz.NewSQLStore(s), func() {
		_ = s.Close()
		cleanup()
	}
}

func TestAuthzSQLStorePostgresContract(t *testing.T) {
	store, cleanup := openPostgresAuthzStore(t)
	defer cleanup()

	ctxA := core.WithTenant(context.Background(), "tenant-a")
	ctxB := core.WithTenant(context.Background(), "tenant-b")
	b := authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: "p1", Condition: `true`}
	if err := store.Grant(ctxA, b); err != nil {
		t.Fatal(err)
	}
	b.Condition = `resource.id == "r1"`
	if err := store.Grant(ctxA, b); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctxA, "bob", "p1")
	if err != nil || len(list) != 1 || list[0].Condition != `resource.id == "r1"` {
		t.Fatalf("list %+v err %v", list, err)
	}
	if err := store.Revoke(ctxA, "bob", authz.RoleViewer, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctxA, "bob", authz.RoleViewer, "p1"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("revoke missing: %v", err)
	}
	nullBind := authz.Binding{Principal: "carol", Role: authz.RoleMember, Project: "p2"}
	if err := store.Grant(ctxA, nullBind); err != nil {
		t.Fatal(err)
	}
	rows, err := store.BindingsForCheck(ctxA, "carol", "p2")
	if err != nil || len(rows) != 1 {
		t.Fatalf("check rows %+v %v", rows, err)
	}
	if err := store.Grant(ctxA, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: ""}); err != nil {
		t.Fatal(err)
	}
	both, err := store.BindingsForCheck(ctxA, "bob", "p2")
	if err != nil || len(both) < 1 {
		t.Fatalf("project filter %+v", both)
	}
	if err := store.Grant(ctxB, authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: "p1"}); err != nil {
		t.Fatal(err)
	}
	listA, err := store.List(ctxA, "bob", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 0 {
		t.Fatal("cross-tenant read")
	}
}
