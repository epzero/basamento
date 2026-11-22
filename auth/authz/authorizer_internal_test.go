package authz

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

type brokenStore struct{}

func (brokenStore) BindingsForCheck(context.Context, string, string) ([]BindingRow, error) {
	return nil, errors.New("db down")
}
func (brokenStore) Grant(context.Context, Binding) error { return errors.New("db down") }
func (brokenStore) Revoke(context.Context, string, Role, string) error {
	return errors.New("db down")
}
func (brokenStore) List(context.Context, string, string) ([]Binding, error) {
	return nil, errors.New("db down")
}

type countingStore struct {
	inner    bindingStore
	bindings int
}

func (c *countingStore) BindingsForCheck(ctx context.Context, pid, proj string) ([]BindingRow, error) {
	c.bindings++
	return c.inner.BindingsForCheck(ctx, pid, proj)
}

func (c *countingStore) Grant(ctx context.Context, b Binding) error {
	return c.inner.Grant(ctx, b)
}
func (c *countingStore) Revoke(ctx context.Context, pid string, role Role, project string) error {
	return c.inner.Revoke(ctx, pid, role, project)
}
func (c *countingStore) List(ctx context.Context, pid, project string) ([]Binding, error) {
	return c.inner.List(ctx, pid, project)
}

type recordingAudit struct {
	calls  int
	events []platform.AuditEvent
}

func (r *recordingAudit) Record(_ context.Context, e platform.AuditEvent) error {
	r.calls++
	r.events = append(r.events, e)
	return nil
}

func testRoles() map[Role]PermissionSet {
	return cloneRoleTable(rolePermissions)
}

func mustCompiler(t *testing.T, limit uint64) *conditionCompiler {
	t.Helper()
	if limit == 0 {
		limit = defaultCostLimit
	}
	c, err := newConditionCompiler(limit)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func openTestSQLStore(t *testing.T) *psql.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authz.db")
	s, err := psql.Open(t.Context(), "sqlite:///"+path, Migrations())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAuthorizeMemberDenyGrantOneAudit(t *testing.T) {
	sqlStore := openTestSQLStore(t)
	store := NewSQLStore(sqlStore)
	audit := &recordingAudit{}
	ctx := core.WithTenant(context.Background(), "acme")
	if err := store.Grant(ctx, Binding{Principal: "m", Role: RoleMember, Project: ""}); err != nil {
		t.Fatal(err)
	}
	a := &Authorizer{
		store:     store,
		audit:     audit,
		clock:     coretest.NewFakeClock(time.Unix(0, 0)),
		compile:   mustCompiler(t, defaultCostLimit),
		cache:     newConditionCache(8),
		costLimit: defaultCostLimit,
		roles:     testRoles(),
	}
	res := Resource{Project: "p1", Kind: "project", ID: "p1"}
	p := authn.Principal{ID: "m", Tenant: "acme"}
	if err := a.authorize(ctx, p, res, "run.cancel"); err != nil {
		t.Fatalf("run.cancel: %v", err)
	}
	if audit.calls != 0 {
		t.Fatalf("allowed audit %d", audit.calls)
	}
	err := a.authorize(ctx, p, res, "run.cancel", "binding.grant")
	if core.CodeOf(err) != core.CodePermissionDenied {
		t.Fatalf("deny: %v", err)
	}
	if audit.calls != 1 {
		t.Fatalf("deny audit %d", audit.calls)
	}
}

func TestCheckStoreFailureUnavailableNoAudit(t *testing.T) {
	audit := &recordingAudit{}
	a := &Authorizer{
		store:     brokenStore{},
		audit:     audit,
		clock:     coretest.NewFakeClock(time.Unix(0, 0)),
		compile:   mustCompiler(t, defaultCostLimit),
		cache:     newConditionCache(8),
		costLimit: defaultCostLimit,
		roles:     testRoles(),
	}
	ctx := core.WithTenant(context.Background(), "acme")
	err := a.Check(ctx, authn.Principal{ID: "ada", Tenant: "acme"}, "run.get", Resource{Kind: "run", ID: "r1"})
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
	if audit.calls != 0 {
		t.Fatalf("audit calls %d", audit.calls)
	}
}

func TestGrantAuthorityStoreFailureNoAudit(t *testing.T) {
	audit := &recordingAudit{}
	a := &Authorizer{
		store:     brokenStore{},
		audit:     audit,
		clock:     coretest.NewFakeClock(time.Unix(0, 0)),
		compile:   mustCompiler(t, defaultCostLimit),
		cache:     newConditionCache(8),
		costLimit: defaultCostLimit,
		roles:     testRoles(),
	}
	ctx := core.WithTenant(context.Background(), "acme")
	err := a.Grant(ctx, authn.Principal{ID: "ada", Tenant: "acme"}, Binding{
		Principal: "bob", Role: RoleViewer, Project: "p1",
	})
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
	if audit.calls != 0 {
		t.Fatalf("audit calls %d", audit.calls)
	}
}

func TestOneBindingsQueryPerCheck(t *testing.T) {
	sqlStore := openTestSQLStore(t)
	inner := NewSQLStore(sqlStore)
	cs := &countingStore{inner: inner}
	audit := &recordingAudit{}
	a := &Authorizer{
		store:     cs,
		audit:     audit,
		clock:     coretest.NewFakeClock(time.Unix(0, 0)),
		compile:   mustCompiler(t, defaultCostLimit),
		cache:     newConditionCache(8),
		costLimit: defaultCostLimit,
		roles:     testRoles(),
	}
	ctx := core.WithTenant(context.Background(), "acme")
	if err := inner.Grant(ctx, Binding{Principal: "ada", Role: RoleAdmin, Project: ""}); err != nil {
		t.Fatal(err)
	}
	if err := a.Check(ctx, authn.Principal{ID: "ada", Tenant: "acme"}, "run.get", Resource{Kind: "run", ID: "r1"}); err != nil {
		t.Fatalf("check: %v", err)
	}
	if cs.bindings != 1 {
		t.Fatalf("Check bindings calls %d", cs.bindings)
	}
	cs.bindings = 0
	if err := a.Grant(ctx, authn.Principal{ID: "ada", Tenant: "acme"}, Binding{Principal: "bob", Role: RoleViewer, Project: "p1"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if cs.bindings != 1 {
		t.Fatalf("Grant authority bindings calls %d", cs.bindings)
	}
}

type nilBindingsStore struct{}

func (nilBindingsStore) BindingsForCheck(context.Context, string, string) ([]BindingRow, error) {
	return nil, nil
}
func (nilBindingsStore) Grant(context.Context, Binding) error { return errors.New("unused") }
func (nilBindingsStore) Revoke(context.Context, string, Role, string) error {
	return errors.New("unused")
}
func (nilBindingsStore) List(context.Context, string, string) ([]Binding, error) {
	return nil, errors.New("unused")
}

type ctxAudit struct {
	events     []platform.AuditEvent
	lastCtxErr error
	lastTenant core.TenantID
}

func (a *ctxAudit) Record(ctx context.Context, e platform.AuditEvent) error {
	a.lastCtxErr = ctx.Err()
	if t, ok := core.TenantFrom(ctx); ok {
		a.lastTenant = t
	}
	a.events = append(a.events, e)
	return nil
}

func TestAuditSurvivesCancelledContext(t *testing.T) {
	audit := &ctxAudit{}
	a := &Authorizer{
		store:     nilBindingsStore{},
		audit:     audit,
		clock:     coretest.NewFakeClock(time.Unix(0, 0)),
		compile:   mustCompiler(t, defaultCostLimit),
		cache:     newConditionCache(8),
		costLimit: defaultCostLimit,
		roles:     testRoles(),
	}
	tenant := core.TenantID("acme")
	ctx := core.WithTenant(context.Background(), tenant)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	err := a.Check(ctx, authn.Principal{ID: "bob", Tenant: tenant}, "run.get", Resource{Kind: "run", ID: "r1"})
	if core.CodeOf(err) != core.CodePermissionDenied {
		t.Fatalf("err %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("events %d", len(audit.events))
	}
	if audit.lastCtxErr != nil {
		t.Fatalf("audit ctx err %v", audit.lastCtxErr)
	}
	if audit.lastTenant != tenant {
		t.Fatalf("tenant %q", audit.lastTenant)
	}
}

func TestDefaultCostLimitBetweenExprDefault(t *testing.T) {
	sqlStore := openTestSQLStore(t)
	store := NewSQLStore(sqlStore)
	audit := &recordingAudit{}
	a, err := New(Options{Store: store, Audit: audit, Clock: coretest.NewFakeClock(time.Unix(0, 0))})
	if err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	list := make([]any, 150)
	for i := range list {
		list[i] = int64(i + 1)
	}
	cond := `resource.attrs["a"].all(x, resource.attrs["b"].all(y, y > 0))`
	if err := store.Grant(ctx, Binding{Principal: "bob", Role: RoleViewer, Project: "", Condition: cond}); err != nil {
		t.Fatal(err)
	}
	cheap := Resource{Kind: "run", ID: "r1", Attrs: map[string]any{"a": []any{int64(1)}, "b": []any{int64(1)}}}
	if err := a.Check(ctx, authn.Principal{ID: "bob", Tenant: "acme"}, "run.get", cheap); err != nil {
		t.Fatalf("cheap: %v", err)
	}
	heavy := Resource{Kind: "run", ID: "r1", Attrs: map[string]any{"a": list, "b": list}}
	err = a.Check(ctx, authn.Principal{ID: "bob", Tenant: "acme"}, "run.get", heavy)
	if core.CodeOf(err) != core.CodePermissionDenied {
		t.Fatalf("heavy code %v", err)
	}
	if len(audit.events) != 1 || audit.events[0].Reason != ReasonConditionCostExceed {
		t.Fatalf("event %+v", audit.events)
	}
}
