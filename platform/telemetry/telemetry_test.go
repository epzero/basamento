package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	psql "github.com/epzero/basamento/platform/sql"
)

func newFakeClockAt(at time.Time) *coretest.FakeClock {
	return coretest.NewFakeClock(at)
}

func newTestSQLStore(t *testing.T) *psql.Store {
	t.Helper()
	migrations := fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n\n-- +goose Down\nSELECT 1;\n")},
	}
	store, err := psql.Open(t.Context(), "sqlite:///"+filepath.Join(t.TempDir(), "telemetry.db"), migrations)
	if err != nil {
		t.Fatalf("open SQL store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestSpanStore(t *testing.T) *SQLSpanStore {
	t.Helper()
	spans, err := NewSQLSpanStore(t.Context(), newTestSQLStore(t))
	if err != nil {
		t.Fatalf("NewSQLSpanStore: %v", err)
	}
	return spans
}

func newEmbeddedTestSetup(t *testing.T, clock core.Clock) *Setup {
	t.Helper()
	setup, err := New(Config{
		Profile: ProfileEmbedded,
		Clock:   clock,
		Spans:   newTestSpanStore(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = setup.Shutdown(ctx)
	})
	return setup
}
