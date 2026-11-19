package telemetry

import (
	"context"
	"path/filepath"
	"testing"
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
	store, err := psql.Open(t.Context(), "sqlite:///"+filepath.Join(t.TempDir(), "telemetry.db"), psql.Migrations())
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
		_ = setup.Shutdown(context.WithoutCancel(t.Context()))
	})
	return setup
}
