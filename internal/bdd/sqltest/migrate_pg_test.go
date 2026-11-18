package sqltest

import (
	"context"
	"testing"

	psql "github.com/epzero/basamento/platform/sql"
)

func TestPostgresMigrateWithAdvisoryLock(t *testing.T) {
	if !PostgresConfigured() {
		t.Skip("BDD_POSTGRES_URL unset")
	}
	ctx := context.Background()
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
