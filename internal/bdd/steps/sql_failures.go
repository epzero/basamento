package steps

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/sqltest"
	psql "github.com/epzero/basamento/platform/sql"
)

// registerSQLFailureSteps covers the failure and redaction scenarios of
// specs/sql/failures.feature and the URL forms of specs/sql/store.feature.
func registerSQLFailureSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the working directory is the data directory$`, theWorkingDirectoryIsTheDataDirectory)
	sc.Step(`^a SQL store is opened with a "([^"]*)" URL for that address with the query "([^"]*)"$`, sqlStoreWithQuery)
	sc.Step(`^tenant "([^"]*)" stores an item with key "([^"]*)"$`, storeItemCall)
	sc.Step(`^tenant "([^"]*)" stores an item with key "([^"]*)" violating the "([^"]*)" constraint$`, storeViolatingConstraint)
	sc.Step(`^tenant "([^"]*)" writes an item while the database refuses with "([^"]*)"$`, writeWhileRefused)
	sc.Step(`^the database becomes unreachable$`, databaseBecomesUnreachable)
	sc.Step(`^the error carries the URL with "([^"]*)"$`, errorCarriesURL)
	sc.Step(`^the error text contains "([^"]*)"$`, errorTextContains)
	sc.Step(`^the error text does not contain "([^"]*)" or "([^"]*)"$`, errorTextDoesNotContain)
}

func theWorkingDirectoryIsTheDataDirectory(ctx context.Context) error {
	w := world(ctx).SQL
	if w.dataDir == "" {
		return fmt.Errorf("no data directory")
	}
	if w.prevDir == "" {
		prev, err := os.Getwd()
		if err != nil {
			return err
		}
		w.prevDir = prev
	}
	return os.Chdir(w.dataDir)
}

func sqlStoreWithQuery(ctx context.Context, scheme, query string) error {
	w := world(ctx).SQL
	raw := fmt.Sprintf("%s://%s/kit?connect_timeout=1&%s", scheme, w.closedPort, query)
	return tryOpen(ctx, raw, 0, false)
}

func storeItemCall(ctx context.Context, tenant, key string) error {
	w := world(ctx).SQL
	if w.repo == nil {
		if err := ensureOpenStore(ctx, 3); err != nil {
			return err
		}
		w = world(ctx).SQL
	}
	w.lastErr = w.repo.StoreItem(w.tenantCtx(tenant), key, sqltest.StoreOptions{})
	return nil
}

func storeViolatingConstraint(ctx context.Context, tenant, key, rule string) error {
	switch rule {
	case "not-null", "check":
	default:
		return fmt.Errorf("unknown constraint rule %q", rule)
	}
	w := world(ctx).SQL
	if w.repo == nil {
		if err := ensureOpenStore(ctx, 3); err != nil {
			return err
		}
		w = world(ctx).SQL
	}
	w.lastErr = w.repo.StoreItemViolatingConstraint(w.tenantCtx(tenant), key, rule)
	return nil
}

func writeWhileRefused(ctx context.Context, tenant, refusal string) error {
	w := world(ctx).SQL
	if w.repo == nil {
		if err := ensureOpenStore(ctx, 3); err != nil {
			return err
		}
		w = world(ctx).SQL
	}
	var release func()
	var err error
	if w.profile == "cloud" {
		err = refusePostgresWrite(ctx, w, refusal)
	} else {
		release, err = holdSQLiteWriteLock(ctx, w, refusal)
	}
	if err != nil {
		return err
	}
	if release != nil {
		defer release()
	}
	w.lastErr = w.repo.StoreItem(w.tenantCtx(tenant), "k-refused", sqltest.StoreOptions{})
	return nil
}

// holdSQLiteWriteLock takes the database's write lock from a second connection
// and turns the store's busy timeout off, so the next write fails at once with
// SQLITE_BUSY (or SQLITE_LOCKED) instead of waiting for the timeout. A busy
// refusal holds an immediate transaction; a locked one holds an exclusive
// lock. The store runs the database in WAL mode, where SQLite treats EXCLUSIVE
// and IMMEDIATE alike, so both refusals reach the SQLITE_BUSY mapping; the
// SQLITE_LOCKED mapping is covered by TestSQLiteLockedMapping in platform/sql.
func holdSQLiteWriteLock(ctx context.Context, w *sqlWorld, refusal string) (func(), error) {
	begin := ""
	switch refusal {
	case "busy":
		begin = `BEGIN IMMEDIATE`
	case "locked":
		begin = `BEGIN EXCLUSIVE`
	default:
		return nil, fmt.Errorf("unknown sqlite refusal %q", refusal)
	}
	if w.dataDir == "" {
		return nil, fmt.Errorf("no sqlite data directory")
	}
	if _, err := w.store.Execer().Exec(ctx, `PRAGMA busy_timeout=0`); err != nil {
		return nil, err
	}
	path := sqliteFilePath(w)
	blocker, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	conn, err := blocker.Conn(ctx)
	if err != nil {
		_ = blocker.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		_ = conn.Close()
		_ = blocker.Close()
		return nil, err
	}
	return func() {
		_ = conn.Close()
		_ = blocker.Close()
	}, nil
}

// sqliteFilePath is the database file of the scenario's SQLite store.
func sqliteFilePath(w *sqlWorld) string {
	path := filepath.Join(w.dataDir, "kit.db")
	if !strings.HasPrefix(w.dbURL, "sqlite:///") {
		return path
	}
	path = strings.TrimPrefix(w.dbURL, "sqlite:///")
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return filepath.Join(w.dataDir, "kit.db")
		}
		path = abs
	}
	return path
}

// refusePostgresWrite installs a trigger on items that raises the SQLSTATE of
// the named refusal. The error then comes from the server and is mapped like
// any other Postgres error.
func refusePostgresWrite(ctx context.Context, w *sqlWorld, refusal string) error {
	state, ok := map[string]string{
		"serialization failure": "40001",
		"deadlock":              "40P01",
		"out of resources":      "53200",
	}[refusal]
	if !ok {
		return fmt.Errorf("unknown postgres refusal %q", refusal)
	}
	e := w.store.Execer()
	body := `CREATE OR REPLACE FUNCTION bdd_refuse() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'bdd refusal' USING ERRCODE = '` + state + `';
  RETURN NEW;
END
$fn$`
	if _, err := e.Exec(ctx, body); err != nil {
		return err
	}
	if _, err := e.Exec(ctx, `DROP TRIGGER IF EXISTS bdd_refuse ON items`); err != nil {
		return err
	}
	_, err := e.Exec(ctx, `CREATE TRIGGER bdd_refuse BEFORE INSERT ON items FOR EACH ROW EXECUTE FUNCTION bdd_refuse()`)
	return err
}

// databaseBecomesUnreachable reopens the cloud store through a local proxy and
// closes the proxy, so the next call cannot reach the server.
func databaseBecomesUnreachable(ctx context.Context) error {
	w := world(ctx).SQL
	if w.profile != "cloud" {
		return nil
	}
	if w.dbURL == "" {
		return fmt.Errorf("no database url")
	}
	proxy, addr, err := sqltest.StartPostgresProxy(w.dbURL)
	if err != nil {
		return err
	}
	u, err := url.Parse(w.dbURL)
	if err != nil {
		proxy.Close()
		return err
	}
	u.Host = addr
	mig, err := sqltest.MigrationsThrough(3, false)
	if err != nil {
		proxy.Close()
		return err
	}
	w.closeStores()
	s, err := psql.Open(ctx, u.String(), mig)
	if err != nil {
		proxy.Close()
		return err
	}
	w.store = s
	w.repo = sqltest.NewSampleRepo(s)
	proxy.Close()
	return nil
}

// sqlCallError is the error of the last store call a scenario made.
func sqlCallError(ctx context.Context) error {
	w := world(ctx).SQL
	for _, err := range []error{w.lastErr, w.lastTxErr, w.openErr} {
		if err != nil {
			return err
		}
	}
	return nil
}

func errorCarriesURL(ctx context.Context, want string) error {
	err := sqlCallError(ctx)
	if err == nil {
		return fmt.Errorf("no error")
	}
	var pe *core.Error
	if errors.As(err, &pe) && pe.Details != nil {
		if v, ok := pe.Details["url"]; ok {
			if got := fmt.Sprint(v); !strings.Contains(got, want) {
				return fmt.Errorf("error url %q does not contain %q", got, want)
			}
			return nil
		}
	}
	if !strings.Contains(err.Error(), want) {
		return fmt.Errorf("error text %q does not contain %q", err.Error(), want)
	}
	return nil
}

func errorTextContains(ctx context.Context, want string) error {
	err := sqlCallError(ctx)
	if err == nil {
		return fmt.Errorf("no error")
	}
	if !strings.Contains(err.Error(), want) {
		return fmt.Errorf("error text %q does not contain %q", err.Error(), want)
	}
	return nil
}

func errorTextDoesNotContain(ctx context.Context, first, second string) error {
	err := sqlCallError(ctx)
	if err == nil {
		return fmt.Errorf("no error")
	}
	text := err.Error()
	if strings.Contains(text, first) || strings.Contains(text, second) {
		return fmt.Errorf("error text %q contains %q or %q", text, first, second)
	}
	return nil
}
