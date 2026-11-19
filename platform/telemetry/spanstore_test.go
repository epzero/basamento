package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

type fakeSQLStore struct{ dialect string }

func (f fakeSQLStore) Dialect() string      { return f.dialect }
func (f fakeSQLStore) Execer() *psql.Execer { return nil }

func TestNewSQLSpanStoreRefusesNonSQLite(t *testing.T) {
	_, err := newSQLSpanStore(t.Context(), fakeSQLStore{dialect: "postgres"}, 0)
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", code)
	}
}

func TestNewSQLSpanStoreRefusesNegativeRetention(t *testing.T) {
	_, err := NewSQLSpanStore(t.Context(), newTestSQLStore(t), -time.Hour)
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", code)
	}
}

func TestNewSQLSpanStoreDefaultsRetention(t *testing.T) {
	store, err := NewSQLSpanStore(t.Context(), newTestSQLStore(t), 0)
	if err != nil {
		t.Fatalf("NewSQLSpanStore: %v", err)
	}
	if store.retention != DefaultRetentionPeriod {
		t.Errorf("retention = %v, want %v", store.retention, DefaultRetentionPeriod)
	}
}

func TestSpanStoreReadsRunInStartThenSpanIDOrder(t *testing.T) {
	store := newTestSpanStore(t)
	ctx := tenantRun("acme", "run_01")
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	spans := []SpanData{
		{TraceID: "t1", SpanID: "sid-b", Name: "step b", Start: base.Add(2 * time.Second), End: base.Add(3 * time.Second), Tenant: "acme", RunID: "run_01"},
		{TraceID: "t1", SpanID: "sid-c", Name: "step c", Start: base, End: base.Add(time.Second), Tenant: "acme", RunID: "run_01"},
		{TraceID: "t1", SpanID: "sid-a", Name: "step a", Start: base.Add(2 * time.Second), End: base.Add(3 * time.Second), Tenant: "acme", RunID: "run_01"},
		{TraceID: "t2", SpanID: "sid-x", Name: "other", Start: base, End: base.Add(time.Second), Tenant: "acme", RunID: "run_02"},
	}
	for _, span := range spans {
		if err := store.Append(ctx, span); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := store.ByRun(ctx, "run_01")
	if err != nil {
		t.Fatalf("ByRun: %v", err)
	}
	want := []string{"step c", "step a", "step b"}
	if len(got) != len(want) {
		t.Fatalf("got %d spans, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("span %d = %s, want %s", i, got[i].Name, want[i])
		}
	}
}

func TestAppendTakesTheTenantFromTheContext(t *testing.T) {
	store := newTestSpanStore(t)
	span := SpanData{
		TraceID: "t1", SpanID: "sid-a", Name: "step a",
		Start:  time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		End:    time.Date(2026, 10, 1, 9, 0, 1, 0, time.UTC),
		Tenant: "acme", RunID: "run_01",
	}
	if err := store.Append(tenantRun("globex", "run_01"), span); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := store.ByRun(tenantRun("globex", "run_01"), "run_01")
	if err != nil {
		t.Fatalf("ByRun: %v", err)
	}
	if len(got) != 1 || got[0].Tenant != "globex" {
		t.Fatalf("globex read %v, want the span written under its context tenant", got)
	}
	if _, err := store.ByRun(tenantRun("acme", "run_01"), "run_01"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("acme read error = %v, want not_found", err)
	}
}

func TestSpanStoreErrorsAreInternal(t *testing.T) {
	store := newTestSQLStore(t)
	spans, err := NewSQLSpanStore(t.Context(), store, 0)
	if err != nil {
		t.Fatalf("NewSQLSpanStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := spans.Append(tenantRun("acme", "run_01"), SpanData{Tenant: "acme", RunID: "run_01", SpanID: "sid-a", Start: at, End: at}); core.CodeOf(err) != core.CodeInternal {
		t.Errorf("Append error = %v, want internal", err)
	}
	if _, err := spans.ByRun(tenantRun("acme", "run_01"), "run_01"); core.CodeOf(err) != core.CodeInternal {
		t.Errorf("ByRun error = %v, want internal", err)
	}
	if _, err := spans.Purge(tenantRun("acme", "run_01"), at); core.CodeOf(err) != core.CodeInternal {
		t.Errorf("Purge error = %v, want internal", err)
	}
}

func TestByRunTiebreakIsByteOrder(t *testing.T) {
	store := newTestSpanStore(t)
	ctx := tenantRun("acme", "run_01")
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"sid-a", "sid-B"} {
		if err := store.Append(ctx, SpanData{
			TraceID: "t1", SpanID: id, Name: id,
			Start: at, End: at.Add(time.Second), Tenant: "acme", RunID: "run_01",
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := store.ByRun(ctx, "run_01")
	if err != nil {
		t.Fatalf("ByRun: %v", err)
	}
	if len(got) != 2 || got[0].SpanID != "sid-B" || got[1].SpanID != "sid-a" {
		t.Fatalf("order = %v, want byte order sid-B before sid-a", got)
	}
}

func TestSpanStoreKeepsAttributes(t *testing.T) {
	store := newTestSpanStore(t)
	ctx := tenantRun("acme", "run_01")
	span := SpanData{
		TraceID:    "t1",
		SpanID:     "sid-a",
		Name:       "step a",
		Start:      time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 10, 1, 9, 0, 0, 250_000_000, time.UTC),
		Status:     StatusError,
		RunID:      "run_01",
		Tenant:     "acme",
		Attributes: map[string]any{"attempt": int64(2), "note": "kept"},
	}
	if err := store.Append(ctx, span); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := store.ByRun(ctx, "run_01")
	if err != nil {
		t.Fatalf("ByRun: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d spans", len(got))
	}
	if got[0].Attributes["note"] != "kept" {
		t.Errorf("note = %v", got[0].Attributes["note"])
	}
	if got[0].Status != StatusError {
		t.Errorf("status = %s", got[0].Status)
	}
	if !got[0].Start.Equal(span.Start) || !got[0].End.Equal(span.End) {
		t.Errorf("times = %v..%v", got[0].Start, got[0].End)
	}
}

func TestSpanStoreHidesAnotherTenantsRun(t *testing.T) {
	store := newTestSpanStore(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := store.Append(tenantRun("acme", "run_01"), SpanData{
		TraceID: "t1", SpanID: "sid-a", Name: "step a",
		Start: at, End: at.Add(time.Second), Tenant: "acme", RunID: "run_01",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := store.ByRun(tenantRun("globex", "run_01"), "run_01")
	if len(got) != 0 {
		t.Fatalf("got %d spans, want 0", len(got))
	}
	if code := core.CodeOf(err); code != core.CodeNotFound {
		t.Fatalf("code = %s, want not_found", code)
	}
}

func TestSpanStoreUnknownRunIsEmptyForTheTenant(t *testing.T) {
	store := newTestSpanStore(t)
	got, err := store.ByRun(tenantRun("acme", "run_99"), "run_99")
	if err != nil {
		t.Fatalf("ByRun: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d spans, want 0", len(got))
	}
}

func TestSpanStorePurgesOnlyExpiredSpans(t *testing.T) {
	store := newTestSpanStore(t)
	ctx := tenantRun("acme", "run_01")
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	old := SpanData{TraceID: "t1", SpanID: "sid-old", Name: "old", Start: base.Add(-2 * time.Hour), End: base.Add(-25 * time.Hour), Tenant: "acme", RunID: "run_01"}
	fresh := SpanData{TraceID: "t1", SpanID: "sid-new", Name: "fresh", Start: base, End: base.Add(-time.Minute), Tenant: "acme", RunID: "run_01"}
	for _, span := range []SpanData{old, fresh} {
		if err := store.Append(ctx, span); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	n, err := store.Purge(ctx, base)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
	got, err := store.ByRun(ctx, "run_01")
	if err != nil {
		t.Fatalf("ByRun: %v", err)
	}
	if len(got) != 1 || got[0].Name != "fresh" {
		t.Errorf("left %v", got)
	}
}

func TestPurgeSpansUsesConfiguredRetention(t *testing.T) {
	clock := newFakeClockAt(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC))
	store, err := NewSQLSpanStore(t.Context(), newTestSQLStore(t), 24*time.Hour)
	if err != nil {
		t.Fatalf("NewSQLSpanStore: %v", err)
	}
	setup, err := New(Config{Profile: ProfileEmbedded, Clock: clock, Spans: store, RetentionPeriod: 24 * time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := tenantRun("acme", "run_01")
	if err := store.Append(ctx, SpanData{
		TraceID: "t1", SpanID: "sid-a", Name: "step a",
		Start: clock.Now().Add(-2 * time.Hour), End: clock.Now().Add(-25 * time.Hour),
		Tenant: "acme", RunID: "run_01",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	n, err := setup.PurgeSpans(ctx)
	if err != nil {
		t.Fatalf("PurgeSpans: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
}

func tenantRun(tenant, run string) context.Context {
	ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
	return WithRunID(ctx, run)
}
