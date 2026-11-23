package authz_test

import (
	"context"
	"testing"

	"github.com/epzero/basamento/auth/authz"
)

func TestStoreIsolationMatrix(t *testing.T) {
	sqlStore := openAuthzDB(t)
	s := authz.NewSQLStore(sqlStore)
	ctxAcme := tenantCtx(t, "acme")
	ctxGlobex := tenantCtx(t, "globex")
	seed := func(ctx context.Context, condTW, condP1, condP2 string) {
		t.Helper()
		mustStoreGrant(t, ctx, s, authz.Binding{Principal: "u", Role: authz.RoleMember, Project: "", Condition: condTW})
		mustStoreGrant(t, ctx, s, authz.Binding{Principal: "u", Role: authz.RoleViewer, Project: "p1", Condition: condP1})
		mustStoreGrant(t, ctx, s, authz.Binding{Principal: "u", Role: authz.RoleAdmin, Project: "p2", Condition: condP2})
	}
	seed(ctxAcme, "acme-tw", "acme-p1", "acme-p2")
	seed(ctxGlobex, "globex-tw", "globex-p1", "globex-p2")

	assertRows := func(ctx context.Context, project string, wantProjects int, wantCond string) {
		t.Helper()
		rows, err := s.BindingsForCheck(ctx, "u", project)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != wantProjects {
			t.Fatalf("BindingsForCheck(%q) got %d rows", project, len(rows))
		}
		if wantCond != "" && project == "p1" {
			found := false
			for _, r := range rows {
				if r.Project == "p1" && r.Condition.String == wantCond {
					found = true
				}
			}
			if !found {
				t.Fatalf("condition %q missing in %+v", wantCond, rows)
			}
		}
	}
	assertRows(ctxAcme, "p1", 2, "acme-p1")
	assertRows(ctxAcme, "", 1, "")
	assertRows(ctxGlobex, "p1", 2, "globex-p1")

	listP1, err := s.List(ctxAcme, "u", "p1")
	if err != nil || len(listP1) != 2 {
		t.Fatalf("list acme p1 %+v %v", listP1, err)
	}
	foundP1 := false
	for _, b := range listP1 {
		if b.Project == "p1" && b.Condition == "acme-p1" {
			foundP1 = true
		}
		if b.Project == "p2" {
			t.Fatal("p2 in list p1")
		}
	}
	if !foundP1 {
		t.Fatal("missing acme-p1 in list")
	}
	listTW, err := s.List(ctxAcme, "u", "")
	if err != nil || len(listTW) != 1 {
		t.Fatalf("list acme tw %+v", listTW)
	}

	if err := s.Revoke(ctxGlobex, "u", authz.RoleViewer, "p1"); err != nil {
		t.Fatal(err)
	}
	assertRows(ctxAcme, "p1", 2, "acme-p1")

	mustStoreGrant(t, ctxGlobex, s, authz.Binding{Principal: "u", Role: authz.RoleViewer, Project: "p1", Condition: "globex-new"})
	assertRows(ctxAcme, "p1", 2, "acme-p1")
	assertRows(ctxGlobex, "p1", 2, "globex-new")

	rowsP2, err := s.BindingsForCheck(ctxAcme, "u", "p1")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rowsP2 {
		if r.Project == "p2" {
			t.Fatal("p2 row in p1 query")
		}
	}
}
