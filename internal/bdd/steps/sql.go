package steps

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/sqltest"
	psql "github.com/epzero/basamento/platform/sql"
)

func registerSQL(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).SQL; w != nil {
			w.teardown()
		}
		return ctx, nil
	})
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		w := world(ctx)
		if w.SQL == nil {
			w.SQL = newSQLWorld()
		} else {
			w.SQL.teardown()
			w.SQL = newSQLWorld()
		}
		if err := skipByProfile(scn, w.SQL.profile); err != nil {
			return ctx, err
		}
		return ctx, nil
	})

	sc.Step(`^an empty data directory$`, func(ctx context.Context) error {
		dir, err := os.MkdirTemp("", "sql-data-*")
		if err != nil {
			return err
		}
		world(ctx).SQL.dataDir = dir
		return nil
	})

	sc.Step(`^an empty database$`, func(ctx context.Context) error {
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
	})

	sc.Step(`^a database server address where nothing listens$`, func(ctx context.Context) error {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		world(ctx).SQL.closedPort = addr
		return nil
	})

	sc.Step(`^a SQL store is opened with URL "([^"]*)" and the sample migrations 1 to 3$`, func(ctx context.Context, raw string) error {
		return openStoreWithMigrations(ctx, expandData(raw, world(ctx).SQL.dataDir), 3, false)
	})

	sc.Step(`^a SQL store is opened with the sample migrations 1 to 3 and the broken migration 4$`, func(ctx context.Context) error {
		w := world(ctx).SQL
		return openStoreWithMigrations(ctx, w.dbURL, 4, true)
	})

	sc.Step(`^a SQL store is opened with URL "([^"]*)" and the sample migrations 1 to 2$`, func(ctx context.Context, raw string) error {
		return openStoreWithMigrations(ctx, expandData(raw, world(ctx).SQL.dataDir), 2, false)
	})

	sc.Step(`^a SQL store is opened with the sample migrations (\d+) to (\d+)$`, func(ctx context.Context, from, to int) error {
		_ = from
		w := world(ctx).SQL
		if w.dbURL == "" {
			u := w.sqliteFileURL("kit.db")
			if w.dataDir == "" {
				if err := stepsEmptyDB(ctx); err != nil {
					return err
				}
				w = world(ctx).SQL
			}
			if w.dbURL == "" {
				w.dbURL = u
			}
		}
		return openStoreWithMigrations(ctx, w.dbURL, to, false)
	})

	sc.Step(`^a SQL store is opened with a "postgres" URL for that address and the sample migrations 1 to 3$`, func(ctx context.Context) error {
		w := world(ctx).SQL
		raw := sqltest.UnreachablePostgresURL(w.closedPort)
		return tryOpen(ctx, raw, 3, false)
	})

	sc.Step(`^a SQL store is opened with a "([^"]*)" URL for that address with user "([^"]*)" and password "([^"]*)"$`, func(ctx context.Context, scheme, user, password string) error {
		w := world(ctx).SQL
		var raw string
		switch scheme {
		case "postgres":
			raw = fmt.Sprintf("postgres://%s@%s/kit?connect_timeout=1", url.UserPassword(user, password), w.closedPort)
		case "mysql":
			raw = fmt.Sprintf("mysql://%s@%s/kit", url.UserPassword(user, password), w.closedPort)
		default:
			raw = scheme + "://" + user
		}
		return tryOpen(ctx, raw, 0, false)
	})

	sc.Step(`^(\d+) SQL stores open it at the same time with the sample migrations 1 to 3$`, func(ctx context.Context, n int) error {
		w := world(ctx).SQL
		mig, err := sqltest.MigrationsThrough(3, false)
		if err != nil {
			return err
		}
		var wg sync.WaitGroup
		errs := make([]error, n)
		stores := make([]*psql.Store, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				s, err := psql.Open(ctx, w.dbURL, mig)
				stores[i] = s
				errs[i] = err
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		w.store = stores[0]
		w.repo = sqltest.NewSampleRepo(w.store)
		w.extraStores = stores[1:]
		return nil
	})

	sc.Step(`^a database migrated with the sample migrations 1 to (\d+)$`, func(ctx context.Context, max int) error {
		w := world(ctx).SQL
		if w.dbURL == "" {
			if err := stepsEmptyDB(ctx); err != nil {
				return err
			}
			w = world(ctx).SQL
		}
		mig, err := sqltest.MigrationsThrough(max, false)
		if err != nil {
			return err
		}
		s, err := psql.Open(ctx, w.dbURL, mig)
		if err != nil {
			return err
		}
		_ = s.Close()
		return nil
	})

	sc.Step(`^a SQL store opened with the sample migrations 1 to 3$`, func(ctx context.Context) error {
		return stepsOpenStore(ctx, 3)
	})

	sc.Step(`^the store opens$`, func(ctx context.Context) error {
		if world(ctx).SQL.store == nil {
			return fmt.Errorf("store did not open")
		}
		return nil
	})

	sc.Step(`^both stores open$`, func(ctx context.Context) error {
		w := world(ctx).SQL
		for i, s := range append([]*psql.Store{w.store}, w.extraStores...) {
			if s == nil {
				return fmt.Errorf("store %d did not open", i)
			}
		}
		return nil
	})

	sc.Step(`^opening fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := world(ctx).SQL.openErr
		if err == nil {
			return fmt.Errorf("opening succeeded, want %s", code)
		}
		if string(core.CodeOf(err)) != code {
			return fmt.Errorf("got code %s, want %s", core.CodeOf(err), code)
		}
		return nil
	})

	sc.Step(`^the error is marked retryable$`, func(ctx context.Context) error {
		if !core.IsRetryable(world(ctx).SQL.openErr) {
			return fmt.Errorf("error is not retryable")
		}
		return nil
	})

	sc.Step(`^neither the error message nor its details contain "([^"]*)"$`, func(ctx context.Context, secret string) error {
		err := world(ctx).SQL.openErr
		if err == nil {
			return fmt.Errorf("no error")
		}
		if psql.ErrorContainsSecret(err, secret) {
			return fmt.Errorf("password in message or details")
		}
		return nil
	})

	sc.Step(`^the data directory contains the file "([^"]*)"$`, func(ctx context.Context, name string) error {
		p := filepath.Join(world(ctx).SQL.dataDir, name)
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("missing file %s: %w", name, err)
		}
		return nil
	})

	sc.Step(`^the data directory is still empty$`, func(ctx context.Context) error {
		entries, err := os.ReadDir(world(ctx).SQL.dataDir)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("data directory has %d entries", len(entries))
		}
		return nil
	})

	sc.Step(`^the schema version is (\d+)$`, func(ctx context.Context, want int) error {
		ver, err := inspectSchemaVersion(ctx, want)
		if err != nil {
			return err
		}
		if int(ver) != want {
			return fmt.Errorf("schema version %d, want %d", ver, want)
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" has exactly (\d+) item with key "([^"]*)"$`, countItem)
	sc.Step(`^tenant "([^"]*)" has exactly (\d+) items with key "([^"]*)"$`, countItem)

	sc.Step(`^tenant "([^"]*)" has no item with key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		if world(ctx).SQL.repo == nil {
			if _, err := inspectRepo(ctx, 3); err != nil {
				return err
			}
		}
		w := world(ctx).SQL
		n, err := w.repo.CountItemsByKey(w.tenantCtx(tenant), key)
		if err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("tenant %s has %d items with key %s", tenant, n, key)
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" has deleted its item with key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		w := world(ctx).SQL
		if w.repo == nil {
			if err := ensureOpenStore(ctx, 3); err != nil {
				return err
			}
		}
		return w.repo.DeleteItemByKey(w.tenantCtx(tenant), key)
	})

	sc.Step(`^tenant "([^"]*)" has stored an item with key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		return storeItem(ctx, tenant, key, sqltest.StoreOptions{})
	})

	sc.Step(`^tenant "([^"]*)" has stored an item with key "([^"]*)" and note "([^"]*)"$`, func(ctx context.Context, tenant, key, note string) error {
		return storeItem(ctx, tenant, key, sqltest.StoreOptions{Note: note})
	})

	sc.Step(`^tenant "([^"]*)" has stored items with keys "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, tenant, k1, k2, k3, k4, k5 string) error {
		for _, k := range []string{k1, k2, k3, k4, k5} {
			if err := storeItem(ctx, tenant, k, sqltest.StoreOptions{}); err != nil {
				return err
			}
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" has stored items with keys "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, tenant, k1, k2 string) error {
		for _, k := range []string{k1, k2} {
			if err := storeItem(ctx, tenant, k, sqltest.StoreOptions{}); err != nil {
				return err
			}
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" has a list "([^"]*)" holding the item with key "([^"]*)"$`, func(ctx context.Context, tenant, list, key string) error {
		w := world(ctx).SQL
		if w.repo == nil {
			if err := ensureOpenStore(ctx, 3); err != nil {
				return err
			}
		}
		n, err := w.repo.CountItemsByKey(w.tenantCtx(tenant), key)
		if err != nil {
			return err
		}
		if n > 0 {
			got, err := w.repo.ItemListName(w.tenantCtx(tenant), key)
			if err != nil {
				return err
			}
			if got != list {
				return fmt.Errorf("item %s in list %q, want %q", key, got, list)
			}
			return nil
		}
		return storeItem(ctx, tenant, key, sqltest.StoreOptions{ListName: list})
	})

	sc.Step(`^tenant "([^"]*)" runs a transaction that stores items with keys "([^"]*)" and "([^"]*)" and then fails$`, func(ctx context.Context, tenant, k1, k2 string) error {
		w := world(ctx).SQL
		w.lastTxErr = w.repo.TxStoreItems(w.tenantCtx(tenant), []string{k1, k2}, true)
		return nil
	})

	sc.Step(`^tenant "([^"]*)" runs a transaction that stores items with keys "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, tenant, k1, k2 string) error {
		w := world(ctx).SQL
		w.lastTxErr = w.repo.TxStoreItems(w.tenantCtx(tenant), []string{k1, k2}, false)
		return nil
	})

	sc.Step(`^the transaction fails with the error of its work$`, func(ctx context.Context) error {
		if world(ctx).SQL.lastTxErr == nil {
			return fmt.Errorf("transaction succeeded")
		}
		return nil
	})

	sc.Step(`^the transaction succeeds$`, func(ctx context.Context) error {
		if world(ctx).SQL.lastTxErr != nil {
			return world(ctx).SQL.lastTxErr
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" stores an item with key "([^"]*)" and note "([^"]*)"$`, func(ctx context.Context, tenant, key, note string) error {
		w := world(ctx).SQL
		w.lastErr = w.repo.StoreItem(w.tenantCtx(tenant), key, sqltest.StoreOptions{Note: note})
		return nil
	})

	sc.Step(`^tenant "([^"]*)" stores an item with key "([^"]*)" in a list that does not exist$`, func(ctx context.Context, tenant, key string) error {
		w := world(ctx).SQL
		w.lastErr = w.repo.StoreItemInMissingList(w.tenantCtx(tenant), key)
		return nil
	})

	sc.Step(`^tenant "([^"]*)" deletes the list "([^"]*)"$`, func(ctx context.Context, tenant, list string) error {
		w := world(ctx).SQL
		w.lastErr = w.repo.DeleteList(w.tenantCtx(tenant), list)
		return nil
	})

	sc.Step(`^tenant "([^"]*)" stores an item with key "([^"]*)" at "([^"]*)"$`, func(ctx context.Context, tenant, key, atStr string) error {
		at, err := time.Parse(time.RFC3339Nano, atStr)
		if err != nil {
			at, err = time.Parse(time.RFC3339, atStr)
			if err != nil {
				return err
			}
		}
		w := world(ctx).SQL
		w.lastErr = w.repo.StoreItem(w.tenantCtx(tenant), key, sqltest.StoreOptions{At: at})
		return nil
	})

	sc.Step(`^the item of tenant "([^"]*)" with key "([^"]*)" is at "([^"]*)"$`, func(ctx context.Context, tenant, key, want string) error {
		at, err := world(ctx).SQL.repo.ItemAt(world(ctx).SQL.tenantCtx(tenant), key)
		if err != nil {
			return err
		}
		got := at.Format(time.RFC3339Nano)
		wantAt, _ := time.Parse(time.RFC3339, want)
		if !at.Equal(wantAt) {
			return fmt.Errorf("got %s, want %s", got, want)
		}
		return nil
	})

	sc.Step(`^the item of tenant "([^"]*)" with key "([^"]*)" has note "([^"]*)"$`, func(ctx context.Context, tenant, key, note string) error {
		got, err := world(ctx).SQL.repo.ItemNote(world(ctx).SQL.tenantCtx(tenant), key)
		if err != nil {
			return err
		}
		if got != note {
			return fmt.Errorf("note %q, want %q", got, note)
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" lists its items ordered by key$`, func(ctx context.Context, tenant string) error {
		keys, err := world(ctx).SQL.repo.ListItemKeys(world(ctx).SQL.tenantCtx(tenant))
		if err != nil {
			return err
		}
		world(ctx).SQL.keyOrder = keys
		return nil
	})

	sc.Step(`^the keys are, in order, "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, k1, k2, k3, k4, k5, k6 string) error {
		want := []string{k1, k2, k3, k4, k5, k6}
		got := world(ctx).SQL.keyOrder
		if len(got) != len(want) {
			return fmt.Errorf("keys %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				return fmt.Errorf("keys %v, want %v", got, want)
			}
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" stores an item with key "([^"]*)" and a note containing a NUL character$`, func(ctx context.Context, tenant, key string) error {
		w := world(ctx).SQL
		w.lastErr = w.repo.StoreItem(w.tenantCtx(tenant), key, sqltest.StoreOptions{Note: "a\x00b"})
		return nil
	})

	sc.Step(`^an item with key "([^"]*)" is stored without a tenant$`, func(ctx context.Context, key string) error {
		w := world(ctx).SQL
		w.lastErr = w.repo.StoreItem(context.Background(), key, sqltest.StoreOptions{})
		return nil
	})

	sc.Step(`^items are listed without a tenant$`, func(ctx context.Context) error {
		w := world(ctx).SQL
		_, w.lastErr = w.repo.ListItemKeys(context.Background())
		return nil
	})

	sc.Step(`^tenant "([^"]*)" lists its items$`, func(ctx context.Context, tenant string) error {
		keys, err := world(ctx).SQL.repo.ListItemKeys(world(ctx).SQL.tenantCtx(tenant))
		if err != nil {
			return err
		}
		world(ctx).SQL.keyOrder = keys
		return nil
	})

	sc.Step(`^the list holds exactly the items with keys "([^"]*)", "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, k1, k2, k3 string) error {
		want := map[string]bool{k1: true, k2: true, k3: true}
		for _, k := range world(ctx).SQL.keyOrder {
			if !want[k] {
				return fmt.Errorf("unexpected key %s", k)
			}
			delete(want, k)
		}
		if len(want) != 0 {
			return fmt.Errorf("missing keys %v", want)
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" reads the item "([^"]*)" of tenant "([^"]*)" by its id$`, func(ctx context.Context, reader, key, owner string) error {
		w := world(ctx).SQL
		id, err := w.repo.LookupIDByKey(w.tenantCtx(owner), key)
		if err != nil {
			return err
		}
		_, w.lastErr = w.repo.GetByID(w.tenantCtx(reader), id)
		return nil
	})

	sc.Step(`^tenant "([^"]*)" changes the key of the item "([^"]*)" of tenant "([^"]*)" to "([^"]*)"$`, func(ctx context.Context, reader, key, owner, newKey string) error {
		w := world(ctx).SQL
		id, err := w.repo.LookupIDByKey(w.tenantCtx(owner), key)
		if err != nil {
			return err
		}
		w.lastErr = w.repo.UpdateKeyByID(w.tenantCtx(reader), id, newKey)
		return nil
	})

	sc.Step(`^tenant "([^"]*)" deletes the item "([^"]*)" of tenant "([^"]*)" by its id$`, func(ctx context.Context, reader, key, owner string) error {
		w := world(ctx).SQL
		id, err := w.repo.LookupIDByKey(w.tenantCtx(owner), key)
		if err != nil {
			return err
		}
		w.lastErr = w.repo.DeleteByID(w.tenantCtx(reader), id)
		return nil
	})

	sc.Step(`^the call fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).SQL
		err := w.lastErr
		if err == nil {
			err = w.lastTxErr
		}
		if err == nil {
			return fmt.Errorf("call succeeded, want %s", code)
		}
		if string(core.CodeOf(err)) != code {
			return fmt.Errorf("got code %s, want %s", core.CodeOf(err), code)
		}
		return nil
	})

	sc.Step(`^no tenant has an item with key "([^"]*)"$`, func(ctx context.Context, key string) error {
		w := world(ctx).SQL
		if w.store == nil {
			if err := ensureOpenStore(ctx, 3); err != nil {
				return err
			}
			w = world(ctx).SQL
		}
		ok, err := anyTenantHasItemKey(ctx, w.store, key)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("found key %s", key)
		}
		return nil
	})
}
