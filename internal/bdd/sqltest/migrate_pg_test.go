package sqltest

import (
	"context"
	"path/filepath"
	"testing"

	psql "github.com/epzero/basamento/platform/sql"
)

func TestPostgresMigrateWithAdvisoryLock(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EP_SUPERVISOR", filepath.Join(root, ".gate/bin/embedded-postgres"))
	if v := PostgresCacheDir(); v == "" {
		t.Setenv(BDDPostgresCacheDir, "/tmp/archer/pgcache")
	}
	resetPostgresSession()
	ctx := context.Background()
	if err := InitPostgres(ctx); err != nil {
		t.Skip(err)
	}
	defer func() { _ = ClosePostgres() }()
	dbURL, cleanup, err := CreateDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	mig, err := MigrationsThrough(1, false)
	if err != nil {
		t.Fatal(err)
	}
	store, err := psql.Open(ctx, dbURL, mig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ver, err := store.SchemaVersion(ctx)
	if err != nil || ver < 1 {
		t.Fatalf("schema version %d err %v", ver, err)
	}
}
