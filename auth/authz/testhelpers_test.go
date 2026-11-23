package authz_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

var testStart = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func openAuthzDB(t *testing.T) *psql.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authz.db")
	s, err := psql.Open(t.Context(), "sqlite:///"+path, authz.Migrations())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func tenantCtx(t *testing.T, id string) context.Context {
	t.Helper()
	return core.WithTenant(t.Context(), core.TenantID(id))
}

type memAudit struct {
	mu     sync.Mutex
	events []platform.AuditEvent
	err    error
	calls  int
}

func (a *memAudit) Record(_ context.Context, e platform.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, e)
	return nil
}

func (a *memAudit) snap() ([]platform.AuditEvent, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]platform.AuditEvent(nil), a.events...), a.calls
}

type countMetric struct {
	mu    sync.Mutex
	total int64
}

func (c *countMetric) Add(_ context.Context, n int64) {
	c.mu.Lock()
	c.total += n
	c.mu.Unlock()
}

type harness struct {
	auth   *authz.Authorizer
	store  *authz.SQLStore
	audit  *memAudit
	clock  *coretest.FakeClock
	metric *countMetric
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sqlStore := openAuthzDB(t)
	audit := &memAudit{}
	clock := coretest.NewFakeClock(testStart)
	metric := &countMetric{}
	a, err := authz.New(authz.Options{
		Store:              authz.NewSQLStore(sqlStore),
		Audit:              audit,
		Clock:              clock,
		AuditFailureMetric: metric,
	})
	if err != nil {
		t.Fatalf("authorizer: %v", err)
	}
	return &harness{
		auth:   a,
		store:  authz.NewSQLStore(sqlStore),
		audit:  audit,
		clock:  clock,
		metric: metric,
	}
}

func principal(id, tenant string) authn.Principal {
	return authn.Principal{ID: id, Tenant: core.TenantID(tenant)}
}

func grantAdmin(t *testing.T, h *harness, tenant, grantor, target, project string, role authz.Role) {
	t.Helper()
	ctx := tenantCtx(t, tenant)
	if err := h.auth.Grant(ctx, principal(grantor, tenant), authz.Binding{
		Principal: target,
		Role:      role,
		Project:   project,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func seedTenantWideAdmin(t *testing.T, h *harness, tenant, id string) {
	t.Helper()
	ctx := tenantCtx(t, tenant)
	mustStoreGrant(t, ctx, h.store, authz.Binding{Principal: id, Role: authz.RoleAdmin, Project: ""})
}

func mustStoreGrant(t *testing.T, ctx context.Context, store *authz.SQLStore, b authz.Binding) {
	t.Helper()
	if err := store.Grant(ctx, b); err != nil {
		t.Fatalf("store grant: %v", err)
	}
}

func mustCheckCode(t *testing.T, err error, want core.Code) {
	t.Helper()
	if core.CodeOf(err) != want {
		t.Fatalf("code %v want %v (%v)", core.CodeOf(err), want, err)
	}
}

func mustDeny(t *testing.T, h *harness, ctx context.Context, p authn.Principal, action string, res authz.Resource) {
	t.Helper()
	err := h.auth.Check(ctx, p, action, res)
	mustCheckCode(t, err, core.CodePermissionDenied)
}
