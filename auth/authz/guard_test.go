package authz_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func TestGuardUndeclaredNoSideEffects(t *testing.T) {
	h := newHarness(t)
	touch := &touchAudit{}
	a, err := authz.New(authz.Options{
		Store: h.store,
		Audit: touch,
		Clock: h.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := authz.NewGuard(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenantCtx(t, "acme")
	err = g.Authorize(ctx, principal("ada", "acme"), "UnknownRPC", nil)
	mustCheckCode(t, err, core.CodeUnsupported)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Message != "procedure is not served" {
		t.Fatalf("message %q", ce.Message)
	}
	if touch.calls != 0 {
		t.Fatal("audit touched")
	}
}

func TestGuardDeclaredRunsCheck(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	seedTenantWideAdmin(t, h, tenant, "ada")
	ctx := tenantCtx(t, tenant)
	g, err := authz.NewGuard(h.auth, authz.Declaration{
		Procedure: "RunGet",
		Action:    "run.get",
		Resolve: func(_ context.Context, _ any) (authz.Resource, error) {
			return authz.Resource{Project: "p1", Kind: "run", ID: "r1"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Authorize(ctx, principal("ada", tenant), "RunGet", struct{}{}); err != nil {
		t.Fatal(err)
	}
}

func TestGuardWiringRefusalMessages(t *testing.T) {
	h := newHarness(t)
	res := func(context.Context, any) (authz.Resource, error) { return authz.Resource{}, nil }
	cases := []struct {
		decl authz.Declaration
		want string
	}{
		{authz.Declaration{Procedure: "", Action: "run.get", Resolve: res}, "procedure name is empty"},
		{authz.Declaration{Procedure: "P", Action: "", Resolve: res}, `procedure "P": action is empty`},
		{authz.Declaration{Procedure: "P", Action: "nope", Resolve: res}, `procedure "P": action is not in the catalog`},
		{authz.Declaration{Procedure: "P", Action: "run.get"}, `procedure "P": resolver is missing`},
	}
	for _, tc := range cases {
		_, err := authz.NewGuard(h.auth, tc.decl)
		var ce *core.Error
		if !errors.As(err, &ce) || !strings.Contains(ce.Message, tc.want) {
			t.Fatalf("decl %+v: err %v want contains %q", tc.decl, err, tc.want)
		}
	}
	_, err := authz.NewGuard(h.auth,
		authz.Declaration{Procedure: "a", Action: "run.get", Resolve: res},
		authz.Declaration{Procedure: "a", Action: "run.list", Resolve: res},
	)
	var ce *core.Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Message, `procedure "a": declared twice`) {
		t.Fatalf("duplicate: %v", err)
	}
}

type touchAudit struct {
	calls int
}

func (t *touchAudit) Record(context.Context, platform.AuditEvent) error {
	t.calls++
	return errors.New("must not be called")
}
