package sql

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationsCreateTheTelemetrySpanTable(t *testing.T) {
	store, err := Open(context.Background(),
		"sqlite:///"+filepath.Join(t.TempDir(), "migrations.db"), Migrations())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var count int
	if err := store.Execer().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM telemetry_spans`).Scan(&count); err != nil {
		t.Fatalf("telemetry_spans is missing: %v", err)
	}
	if count != 0 {
		t.Errorf("new table holds %d rows, want 0", count)
	}
	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}
}
