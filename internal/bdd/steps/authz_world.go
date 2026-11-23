package steps

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/internal/bdd/sqltest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/credentials"
	psql "github.com/epzero/basamento/platform/sql"
)

const authzCredMasterKey = "authz-mk"

// errBDDGuardNotWired is returned when an RPC call runs while NewGuard failed at
// wiring time; it is not a production error code.
var errBDDGuardNotWired = fmt.Errorf("bdd: guard not wired")

type authzBindingSnap struct {
	Principal string
	Project   string
	Role      string
	Condition string
}

type authzWorld struct {
	dir        string
	pgCleanup  func()
	db         *psql.Store
	sqlStore   *authz.SQLStore
	auditSQL   *credentials.AuditStore
	auditWrap  *authzAuditWrap
	auth       *authz.Authorizer
	api        *authzTestAPI
	guard      *authz.Guard
	probeGuard *authz.Guard
	clock      *coretest.FakeClock
	logBuf     *slogRecordingHandler
	metric     *authzCountMetric

	forceSQLite bool
	storeBroken bool

	tenant          string
	principals      map[string]authn.Principal
	resources       map[string]authz.Resource
	lastErr         error
	lastList        []string
	lastNames       []string
	lastNamesJSON   []byte
	lastResolveBody []byte
	credSink        []byte
	credSecret      []byte
	costHeavy       bool
	rpcMode         string
	rpcProc         string

	guardWiringErr  error
	seededCondition string
	bindingsSnap    []authzBindingSnap

	seedAdmin map[string]bool
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

func scenarioUsesAuthz(scn *godog.Scenario) bool {
	return filepath.Base(scn.Uri) == "permissions.feature"
}

// scenarioPinsSQLite forces SQLite when a scenario step text mentions that dialect
// (AUTH-011-01), even on the cloud BDD profile pass.
func scenarioPinsSQLite(scn *godog.Scenario) bool {
	for _, step := range scn.Steps {
		if strings.Contains(step.Text, "on SQLite") {
			return true
		}
	}
	return false
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
	if w.pgCleanup != nil {
		w.pgCleanup()
		w.pgCleanup = nil
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
		w.dir = ""
	}
}

// The authz schema is the same for every scenario, so it is migrated once per
// process into a template database; each scenario starts from a copy instead of
// paying the migration cost for every permission scenario. The suite hit Go's
// 10m default timeout in CI (EP0-115); this mirrors the worker template in
// WP-L3-07.
var (
	authzDBTemplateOnce sync.Once
	authzDBTemplatePath string
	authzDBTemplateErr  error
)

// authzSQLiteTemplate returns the path of a migrated, unopened SQLite database
// that holds the authz schema and the runs fixture table.
func authzSQLiteTemplate() (string, error) {
	authzDBTemplateOnce.Do(func() {
		ctx := context.Background()
		mig, err := combinedAuthMigrations()
		if err != nil {
			authzDBTemplateErr = err
			return
		}
		dir, err := os.MkdirTemp("", "authz-bdd-template-*")
		if err != nil {
			authzDBTemplateErr = err
			return
		}
		dbPath := filepath.Join(dir, "authz.db")
		store, err := psql.Open(ctx, "sqlite:///"+dbPath, mig)
		if err != nil {
			authzDBTemplateErr = err
			return
		}
		if _, err := store.Execer().Exec(ctx, `CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, project TEXT NOT NULL)`); err != nil {
			_ = store.Close()
			authzDBTemplateErr = err
			return
		}
		// Closing checkpoints the WAL, so the main file is a complete copy
		// source; any surviving sidecar is copied with it just in case.
		if err := store.Close(); err != nil {
			authzDBTemplateErr = err
			return
		}
		authzDBTemplatePath = dbPath
	})
	return authzDBTemplatePath, authzDBTemplateErr
}

// copyAuthzDB copies the migrated template to dst, with the WAL sidecar when
// one survived the template's close, so a scenario still gets a real isolated
// on-disk database of its own.
func copyAuthzDB(src, dst string) error {
	for _, suffix := range []string{"", "-wal"} {
		if err := copyFileIfExists(src+suffix, dst+suffix); err != nil {
			return err
		}
	}
	return nil
}

func (w *authzWorld) databaseURL(ctx context.Context) (string, error) {
	if w.forceSQLite || os.Getenv("BDD_SQL_PROFILE") != "cloud" || !sqltest.PostgresAvailable() {
		dir, err := os.MkdirTemp("", "authz-bdd-*")
		if err != nil {
			return "", err
		}
		w.dir = dir
		template, err := authzSQLiteTemplate()
		if err != nil {
			return "", err
		}
		dbPath := filepath.Join(dir, "authz.db")
		if err := copyAuthzDB(template, dbPath); err != nil {
			return "", err
		}
		return "sqlite:///" + dbPath, nil
	}
	url, cleanup, err := sqltest.CreateDatabase(ctx)
	if err != nil {
		return "", err
	}
	w.pgCleanup = cleanup
	return url, nil
}

func (w *authzWorld) ensureStore(ctx context.Context) error {
	if w.storeBroken {
		return nil
	}
	if w.db != nil {
		return nil
	}
	mig, err := combinedAuthMigrations()
	if err != nil {
		return err
	}
	raw, err := w.databaseURL(ctx)
	if err != nil {
		return err
	}
	db, err := psql.Open(ctx, raw, mig)
	if err != nil {
		w.teardown()
		return err
	}
	w.db = db
	w.sqlStore = authz.NewSQLStore(db)
	w.auditSQL = credentials.NewSQLAudit(db)
	w.auditWrap = &authzAuditWrap{inner: w.auditSQL}
	w.api = newAuthzTestAPI(w)
	if _, err := w.db.Execer().Exec(ctx, `CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, project TEXT NOT NULL)`); err != nil {
		return err
	}
	if err := w.rebuildAuthorizer(); err != nil {
		return err
	}
	return w.seedCredentialNames(ctx)
}

func (w *authzWorld) rebuildAuthorizer() error {
	if w.sqlStore == nil {
		return nil
	}
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
	return w.rebuildGuard()
}

func (w *authzWorld) rebuildGuard() error {
	if w.auth == nil || w.api == nil {
		return nil
	}
	good, err := authz.NewGuard(w.auth, w.api.goodDeclarations()...)
	if err != nil {
		return err
	}
	w.probeGuard = good
	decls := w.api.declarations()
	g, err := authz.NewGuard(w.auth, decls...)
	if err != nil {
		w.guard = nil
		w.guardWiringErr = err
		return nil
	}
	w.guard = g
	w.guardWiringErr = nil
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
	w.seededCondition = cond
	return w.sqlStore.Grant(w.tenantCtx(tenant), authz.Binding{
		Principal: pid,
		Role:      authz.RoleViewer,
		Project:   project,
		Condition: cond,
	})
}

func (w *authzWorld) snapshotBindings(tenant string) ([]authzBindingSnap, error) {
	rows, err := w.db.Execer().Query(context.Background(),
		`SELECT principal, project, role, condition FROM authz_bindings WHERE tenant = ? ORDER BY principal, project, role`,
		tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []authzBindingSnap
	for rows.Next() {
		var s authzBindingSnap
		var cond sql.NullString
		if err := rows.Scan(&s.Principal, &s.Project, &s.Role, &cond); err != nil {
			return nil, err
		}
		if cond.Valid {
			s.Condition = cond.String
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (w *authzWorld) principalBindingCount(tenant, principal string) (int, error) {
	rows, err := w.db.Execer().Query(context.Background(),
		`SELECT COUNT(*) FROM authz_bindings WHERE tenant = ? AND principal = ?`,
		tenant, principal)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, fmt.Errorf("no count row")
	}
	var n int
	if err := rows.Scan(&n); err != nil {
		return 0, err
	}
	return n, rows.Err()
}

func (w *authzWorld) readAudits(tenant string) ([]platform.AuditEvent, error) {
	return w.auditSQL.Read(w.tenantCtx(tenant))
}

func (w *authzWorld) recordErr(err error) {
	w.lastErr = err
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

func roleHoldsEveryPermissionOf(holder, contained string) error {
	rHold, holdOk := parseBuiltinRole(holder)
	rCont, contOk := parseBuiltinRole(contained)
	if !holdOk || !contOk {
		return fmt.Errorf("unknown role holder=%q contained=%q", holder, contained)
	}
	have := make(map[string]bool, len(rHold.Permissions()))
	for _, a := range rHold.Permissions() {
		have[a] = true
	}
	for _, a := range rCont.Permissions() {
		if !have[a] {
			return fmt.Errorf("%q missing permission %q of %q", holder, a, contained)
		}
	}
	return nil
}

func parseBuiltinRole(name string) (authz.Role, bool) {
	switch authz.Role(name) {
	case authz.RoleViewer, authz.RoleMember, authz.RoleAdmin:
		return authz.Role(name), true
	default:
		return "", false
	}
}

var safeAuditReasons = map[string]bool{
	authz.ReasonNoBinding:           true,
	authz.ReasonTenantMismatch:      true,
	authz.ReasonConditionError:      true,
	authz.ReasonConditionCostExceed: true,
	authz.ReasonScopeNarrowed:       true,
	authz.ReasonUndeclaredAction:    true,
}

func auditStringFields(e platform.AuditEvent) []string {
	return []string{e.Principal, e.Action, e.Resource, e.Outcome, e.Reason, e.RunID, e.Step}
}

func bindingsEqual(a, b []authzBindingSnap) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedRunIDs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
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

func authzSortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
