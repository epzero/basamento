package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/sqltest"
	psql "github.com/epzero/basamento/platform/sql"
)

type sqlWorld struct {
	profile     string
	dataDir     string
	dbURL       string
	pgCleanup   func()
	store       *psql.Store
	repo        *sqltest.SampleRepo
	lastErr     error
	openErr     error
	lastTxErr   error
	itemIDs     map[string]string // key "tenant:key" -> id
	closedPort  string
	keyOrder    []string
	extraStores []*psql.Store
}

func newSQLWorld() *sqlWorld {
	profile := os.Getenv("BDD_SQL_PROFILE")
	if profile == "" {
		profile = "embedded"
	}
	return &sqlWorld{profile: profile, itemIDs: map[string]string{}}
}

func (w *sqlWorld) tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func (w *sqlWorld) sqliteFileURL(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.dataDir, path)
	}
	return "sqlite:///" + path
}

func (w *sqlWorld) closeStores() {
	if w.store != nil {
		_ = w.store.Close()
		w.store = nil
		w.repo = nil
	}
	for _, s := range w.extraStores {
		_ = s.Close()
	}
	w.extraStores = nil
}

func (w *sqlWorld) teardown() {
	w.closeStores()
	if w.pgCleanup != nil {
		w.pgCleanup()
		w.pgCleanup = nil
	}
}

func expandData(raw, dataDir string) string {
	return strings.ReplaceAll(raw, "{data}", dataDir)
}

func openStoreWithMigrations(ctx context.Context, raw string, max int, broken bool) error {
	w := world(ctx).SQL
	w.closeStores()
	mig, err := sqltest.MigrationsThrough(max, broken)
	if err != nil {
		return err
	}
	if strings.HasPrefix(raw, "sqlite://") {
		w.dbURL = raw
	} else if w.profile == "cloud" && !strings.HasPrefix(raw, "postgres") {
		if w.dbURL != "" {
			raw = w.dbURL
		}
	}
	s, err := psql.Open(ctx, raw, mig)
	if err != nil {
		w.openErr = err
		return nil
	}
	w.store = s
	w.repo = sqltest.NewSampleRepo(s)
	w.openErr = nil
	if strings.HasPrefix(raw, "sqlite:") {
		w.dbURL = raw
	}
	return nil
}

func tryOpen(ctx context.Context, raw string, max int, broken bool) error {
	w := world(ctx).SQL
	w.closeStores()
	mig, err := sqltest.MigrationsThrough(maxOr(max, 3), broken)
	if err != nil {
		return err
	}
	s, err := psql.Open(ctx, raw, mig)
	w.openErr = err
	if err != nil {
		return nil
	}
	_ = s.Close()
	return nil
}

func maxOr(max, def int) int {
	if max > 0 {
		return max
	}
	return def
}

func inspectSchemaVersion(ctx context.Context, max int) (int64, error) {
	w := world(ctx).SQL
	if w.store != nil {
		return w.store.SchemaVersion(ctx)
	}
	mig, err := sqltest.MigrationsThrough(max, false)
	if err != nil {
		return 0, err
	}
	s, err := psql.Open(ctx, w.dbURL, mig)
	if err != nil {
		return 0, err
	}
	defer func() { _ = s.Close() }()
	return s.SchemaVersion(ctx)
}

func inspectRepo(ctx context.Context, max int) (*sqltest.SampleRepo, error) {
	w := world(ctx).SQL
	if w.repo != nil {
		return w.repo, nil
	}
	if err := openStoreWithMigrations(ctx, w.dbURL, max, false); err != nil {
		return nil, err
	}
	return world(ctx).SQL.repo, nil
}

func stepsOpenStore(ctx context.Context, max int) error {
	w := world(ctx).SQL
	if w.dbURL == "" {
		if err := stepsEmptyDB(ctx); err != nil {
			return err
		}
		w = world(ctx).SQL
	}
	return openStoreWithMigrations(ctx, w.dbURL, max, false)
}

func stepsEmptyDB(ctx context.Context) error {
	w := world(ctx).SQL
	if w.profile == "cloud" {
		u, cleanup, err := sqltest.CreateDatabase(ctx)
		if err != nil {
			return err
		}
		w.dbURL = u
		w.pgCleanup = cleanup
		return nil
	}
	dir, err := os.MkdirTemp("", "sql-sqlite-*")
	if err != nil {
		return err
	}
	w.dataDir = dir
	w.dbURL = w.sqliteFileURL("kit.db")
	return nil
}

func ensureOpenStore(ctx context.Context, max int) error {
	w := world(ctx).SQL
	if w.store != nil {
		return nil
	}
	return openStoreWithMigrations(ctx, w.dbURL, max, false)
}

func storeItem(ctx context.Context, tenant, key string, opts sqltest.StoreOptions) error {
	w := world(ctx).SQL
	if w.repo == nil {
		if err := ensureOpenStore(ctx, 3); err != nil {
			return err
		}
	}
	return w.repo.StoreItem(w.tenantCtx(tenant), key, opts)
}

func countItem(ctx context.Context, tenant string, n int, key string) error {
	if world(ctx).SQL.repo == nil {
		if _, err := inspectRepo(ctx, 3); err != nil {
			return err
		}
	}
	w := world(ctx).SQL
	got, err := w.repo.CountItemsByKey(w.tenantCtx(tenant), key)
	if err != nil {
		return err
	}
	if got != n {
		return fmt.Errorf("tenant %s has %d items with key %s, want %d", tenant, got, key, n)
	}
	return nil
}

// anyTenantHasItemKey reports whether any row carries key, for cross-tenant conformance steps.
func anyTenantHasItemKey(ctx context.Context, store *psql.Store, key string) (bool, error) {
	var n int
	err := store.Execer().QueryRow(ctx, `SELECT COUNT(*) FROM items WHERE key = ?`, key).Scan(&n)
	return n > 0, err
}

func skipByProfile(scn *godog.Scenario, profile string) error {
	tags := scn.Tags
	hasEmbedded := false
	hasCloud := false
	for _, t := range tags {
		if t.Name == "@profile:embedded" {
			hasEmbedded = true
		}
		if t.Name == "@profile:cloud" {
			hasCloud = true
		}
	}
	if profile == "embedded" && hasCloud && !hasEmbedded {
		return godog.ErrSkip
	}
	if profile == "cloud" {
		if hasEmbedded && !hasCloud {
			return godog.ErrSkip
		}
		if !sqltest.PostgresConfigured() {
			return godog.ErrSkip
		}
	}
	return nil
}
