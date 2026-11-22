package steps

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/credentials"
	psql "github.com/epzero/basamento/platform/sql"
)

const authzCredMasterKey = "authz-mk"

type authzWorld struct {
	dir       string
	db        *psql.Store
	sqlStore  *authz.SQLStore
	auditSQL  *credentials.AuditStore
	auditWrap *authzAuditWrap
	auth      *authz.Authorizer
	api       *authzTestAPI
	guard     *authz.Guard
	clock     *coretest.FakeClock
	logBuf    *slogRecordingHandler
	metric    *authzCountMetric

	tenant      string
	principals  map[string]authn.Principal
	resources   map[string]authz.Resource
	lastErr     error
	lastDenied  bool
	lastAllowed bool
	lastList    []string
	lastNames   []string
	credSink    []byte
	dbClosed    bool
	costHeavy   bool
	rpcMode     string // "", "no-action", "no-resolver"
	rpcProc     string

	seedAdmin map[string]bool // tenant -> seeded tenant-wide admin
}

type authzCountMetric struct {
	mu    sync.Mutex
	total int64
}

func (c *authzCountMetric) Add(_ context.Context, n int64) {
	c.mu.Lock()
	c.total += n
	c.mu.Unlock()
}

func (c *authzCountMetric) get() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

type slogRecordingHandler struct {
	mu   sync.Mutex
	logs []slog.Record
}

func (h *slogRecordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *slogRecordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, r)
	return nil
}

func (h *slogRecordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }

func (h *slogRecordingHandler) WithGroup(string) slog.Handler { return h }

func (h *slogRecordingHandler) records() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.logs...)
}

// authzAuditWrap counts Record calls and can reject every record (AUTH-025).
type authzAuditWrap struct {
	inner  platform.Audit
	reject bool
	calls  int
	mu     sync.Mutex
}

func (a *authzAuditWrap) Record(ctx context.Context, e platform.AuditEvent) error {
	a.mu.Lock()
	a.calls++
	reject := a.reject
	a.mu.Unlock()
	if reject {
		return core.NewError(core.CodeUnavailable, "audit store unavailable", nil)
	}
	return a.inner.Record(ctx, e)
}

func newAuthzWorld() *authzWorld {
	return &authzWorld{
		tenant:     "acme",
		principals: map[string]authn.Principal{},
		resources:  map[string]authz.Resource{},
		seedAdmin:  map[string]bool{},
		metric:     &authzCountMetric{},
		logBuf:     &slogRecordingHandler{},
		clock:      coretest.NewFakeClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)),
	}
}

func authzW(ctx context.Context) *authzWorld {
	w := world(ctx)
	if w.Authz == nil {
		w.Authz = newAuthzWorld()
	}
	return w.Authz
}

func (w *authzWorld) teardown() {
	if w.db != nil {
		_ = w.db.Close()
		w.db = nil
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
		w.dir = ""
	}
}

func (w *authzWorld) ensureStore(ctx context.Context) error {
	if w.db != nil {
		return nil
	}
	mig, err := combinedAuthMigrations()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "authz-bdd-*")
	if err != nil {
		return err
	}
	w.dir = dir
	path := filepath.Join(dir, "authz.db")
	db, err := psql.Open(ctx, "sqlite:///"+path, mig)
	if err != nil {
		_ = os.RemoveAll(dir)
		w.dir = ""
		return err
	}
	w.db = db
	w.sqlStore = authz.NewSQLStore(db)
	w.auditSQL = credentials.NewSQLAudit(db)
	w.auditWrap = &authzAuditWrap{inner: w.auditSQL}
	w.api = newAuthzTestAPI(w)
	if _, err := w.db.Execer().Exec(ctx, `CREATE TABLE runs (id TEXT PRIMARY KEY, project TEXT NOT NULL)`); err != nil {
		return err
	}
	if err := w.rebuildAuthorizer(); err != nil {
		return err
	}
	return w.seedCredentialNames(ctx)
}

func (w *authzWorld) rebuildAuthorizer() error {
	a, err := authz.New(authz.Options{
		Store:              w.sqlStore,
		Audit:              w.auditWrap,
		Clock:              w.clock,
		Logger:             slog.New(w.logBuf),
		AuditFailureMetric: w.metric,
	})
	if err != nil {
		return err
	}
	w.auth = a
	if w.dbClosed && w.db != nil {
		_ = w.db.Close()
	}
	return w.rebuildGuard()
}

func (w *authzWorld) rebuildGuard() error {
	if w.auth == nil {
		return nil
	}
	if w.api == nil {
		w.api = newAuthzTestAPI(w)
	}
	decls := w.api.declarations()
	g, err := authz.NewGuard(w.auth, decls...)
	if err != nil {
		w.guard = nil
		return nil // wiring tests expect a refused declaration, not a hard failure.
	}
	w.guard = g
	return nil
}

func (w *authzWorld) tenantCtx(tenant string) context.Context {
	if tenant == "" {
		tenant = w.tenant
	}
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func (w *authzWorld) principal(id, tenant string) authn.Principal {
	if tenant == "" {
		tenant = w.tenant
	}
	p, ok := w.principals[id]
	if !ok {
		p = authn.Principal{ID: id, Tenant: core.TenantID(tenant)}
	}
	return p
}

func (w *authzWorld) setPrincipal(id, tenant string, scopes []string) {
	if tenant == "" {
		tenant = w.tenant
	}
	w.principals[id] = authn.Principal{ID: id, Tenant: core.TenantID(tenant), Scopes: scopes}
}

func (w *authzWorld) seedTenantAdmin(_ context.Context, tenant string) error {
	if w.seedAdmin[tenant] {
		return nil
	}
	tctx := w.tenantCtx(tenant)
	// Bootstrap binding via SQL store (no grantor check).
	if err := w.sqlStore.Grant(tctx, authz.Binding{
		Principal: "bootstrap",
		Role:      authz.RoleAdmin,
		Project:   "",
	}); err != nil {
		return err
	}
	w.seedAdmin[tenant] = true
	return nil
}

func (w *authzWorld) seedRole(ctx context.Context, tenant, pid, role, project string) error {
	if err := w.ensureStore(ctx); err != nil {
		return err
	}
	if err := w.seedTenantAdmin(ctx, tenant); err != nil {
		return err
	}
	w.setPrincipal(pid, tenant, nil)
	return w.sqlStore.Grant(w.tenantCtx(tenant), authz.Binding{
		Principal: pid,
		Role:      authz.Role(role),
		Project:   project,
	})
}

func (w *authzWorld) seedCondition(ctx context.Context, tenant, pid, project, cond string) error {
	if err := w.ensureStore(ctx); err != nil {
		return err
	}
	if err := w.seedTenantAdmin(ctx, tenant); err != nil {
		return err
	}
	return w.sqlStore.Grant(w.tenantCtx(tenant), authz.Binding{
		Principal: pid,
		Role:      authz.RoleViewer,
		Project:   project,
		Condition: cond,
	})
}

func (w *authzWorld) readAudits(tenant string) ([]platform.AuditEvent, error) {
	return w.auditSQL.Read(w.tenantCtx(tenant))
}

func (w *authzWorld) recordErr(err error) {
	w.lastErr = err
	w.lastDenied = err != nil
	w.lastAllowed = err == nil
}

func (w *authzWorld) denialMessage() string {
	if w.lastErr == nil {
		return ""
	}
	return w.lastErr.Error()
}

func costHeavyAttrs() map[string]any {
	list := make([]any, 150)
	for i := range list {
		list[i] = int64(i + 1)
	}
	return map[string]any{"a": list, "b": list}
}

func memberContainsViewer() error {
	member := make(map[string]bool)
	for _, a := range authz.RoleMember.Permissions() {
		member[a] = true
	}
	for _, a := range authz.RoleViewer.Permissions() {
		if !member[a] {
			return fmt.Errorf("member missing viewer permission %q", a)
		}
	}
	return nil
}

func logRecordHasAttrs(r slog.Record, key string) (string, bool) {
	var found string
	ok := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found = a.Value.String()
			ok = true
			return false
		}
		return true
	})
	return found, ok
}

func recordContainsField(r slog.Record, sub string) bool {
	var buf bytes.Buffer
	r.Attrs(func(a slog.Attr) bool {
		_, _ = fmt.Fprintf(&buf, "%s=%s ", a.Key, a.Value)
		return true
	})
	return strings.Contains(buf.String(), sub)
}
