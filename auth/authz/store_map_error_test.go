package authz_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	psql "github.com/epzero/basamento/platform/sql"
)

func openClosedAuthzStore(t *testing.T) (*authz.SQLStore, *psql.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "closed.db")
	sqlStore, err := psql.Open(t.Context(), "sqlite:///"+path, authz.Migrations())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	authStore := authz.NewSQLStore(sqlStore)
	if err := sqlStore.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return authStore, sqlStore
}

func assertMappedStoreErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want error")
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("got %T %v", err, err)
	}
	if strings.Contains(strings.ToLower(ce.Message), "closed") {
		t.Fatalf("driver text leaked: %q", ce.Message)
	}
}

func TestSQLStoreMappedErrorsClosedDB(t *testing.T) {
	store, _ := openClosedAuthzStore(t)
	ctx := tenantCtx(t, "acme")
	b := authz.Binding{Principal: "bob", Role: authz.RoleViewer, Project: "p1"}
	assertMappedStoreErr(t, store.Grant(ctx, b))
	assertMappedStoreErr(t, store.Revoke(ctx, "bob", authz.RoleViewer, "p1"))
	_, err := store.List(ctx, "bob", "p1")
	assertMappedStoreErr(t, err)
	_, err = store.BindingsForCheck(ctx, "bob", "p1")
	assertMappedStoreErr(t, err)
}

func TestCheckUnavailableWhenStoreClosed(t *testing.T) {
	sqlStore, err := psql.Open(t.Context(), "sqlite:///"+filepath.Join(t.TempDir(), "c.db"), authz.Migrations())
	if err != nil {
		t.Fatal(err)
	}
	authStore := authz.NewSQLStore(sqlStore)
	audit := &memAudit{}
	a, err := authz.New(authz.Options{
		Store: authStore,
		Audit: audit,
		Clock: coretest.NewFakeClock(testStart),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlStore.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := tenantCtx(t, "acme")
	p := principal("bob", "acme")
	p.Scopes = []string{"openid"}
	err = a.Check(ctx, p, "run.get", authz.Resource{Kind: "run", ID: "r1"})
	mustCheckCode(t, err, core.CodeUnavailable)
	_, n := audit.snap()
	if n != 0 {
		t.Fatalf("audit %d", n)
	}
}
