package telemetry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

// SpanStore keeps the ended spans of the embedded profile and reads them back
// per run (REQ-TELEM-010 through REQ-TELEM-014).
type SpanStore interface {
	// Append keeps one ended span.
	Append(ctx context.Context, span SpanData) error
	// ByRun returns the spans of runID in start order, then span id order, for
	// the tenant of ctx. It fails with not_found when runID belongs to another
	// tenant.
	ByRun(ctx context.Context, runID string) ([]SpanData, error)
	// Purge deletes the spans that ended more than the store's retention
	// period before now and returns the count.
	Purge(ctx context.Context, now time.Time) (int64, error)
}

// RetentionApplier is a SpanStore whose purge window a Setup can set. New
// applies Config.RetentionPeriod to it, so the configured period is the one
// used instead of the value the store was built with (REQ-TELEM-014).
type RetentionApplier interface {
	// SetRetention replaces the purge window. Zero restores
	// DefaultRetentionPeriod; a negative period is refused with
	// invalid_argument.
	SetRetention(retention time.Duration) error
}

// spanSQLStore is the part of a *psql.Store the span store uses. It exists so
// the dialect guard can be tested without a second database.
type spanSQLStore interface {
	Dialect() string
	Execer() *psql.Execer
}

// SQLSpanStore is the SpanStore over an embedded profile platform/sql store.
// The table belongs to this package: it is created if absent, has one shape,
// and is not part of the platform/sql migration set.
type SQLSpanStore struct {
	store     spanSQLStore
	retention time.Duration
}

const spanSchema = `CREATE TABLE IF NOT EXISTS telemetry_spans (
	tenant_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	parent_span_id TEXT NOT NULL,
	name TEXT NOT NULL,
	start_at BIGINT NOT NULL,
	end_at BIGINT NOT NULL,
	status TEXT NOT NULL,
	attrs TEXT NOT NULL,
	PRIMARY KEY (tenant_id, span_id)
)`

const spanIndex = `CREATE INDEX IF NOT EXISTS telemetry_spans_run ON telemetry_spans (run_id, tenant_id, start_at, span_id)`

// NewSQLSpanStore returns a span store on an embedded SQLite store, creating
// telemetry_spans and its run index if they are absent. It refuses a different
// dialect and a negative retention; zero means DefaultRetentionPeriod.
func NewSQLSpanStore(ctx context.Context, store *psql.Store, retention time.Duration) (*SQLSpanStore, error) {
	if store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: nil SQL store", nil)
	}
	return newSQLSpanStore(ctx, store, retention)
}

func newSQLSpanStore(ctx context.Context, store spanSQLStore, retention time.Duration) (*SQLSpanStore, error) {
	if store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: nil SQL store", nil)
	}
	if store.Dialect() != "sqlite" {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: the span store requires SQLite", nil)
	}
	if retention < 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: negative retention period", nil)
	}
	if retention == 0 {
		retention = DefaultRetentionPeriod
	}
	if _, err := store.Execer().Exec(ctx, spanSchema); err != nil {
		return nil, spanStoreError("create span table", err)
	}
	if _, err := store.Execer().Exec(ctx, spanIndex); err != nil {
		return nil, spanStoreError("create span index", err)
	}
	return &SQLSpanStore{store: store, retention: retention}, nil
}

// SetRetention implements RetentionApplier. It must not run concurrently with
// Purge; New applies the configured period before the setup is returned.
func (s *SQLSpanStore) SetRetention(retention time.Duration) error {
	if retention < 0 {
		return core.NewError(core.CodeInvalidArgument, "telemetry: negative retention period", nil)
	}
	if retention == 0 {
		retention = DefaultRetentionPeriod
	}
	s.retention = retention
	return nil
}

// Append implements SpanStore. The tenant comes from ctx when present, so a
// caller cannot write a span under a tenant other than the one it is acting as.
func (s *SQLSpanStore) Append(ctx context.Context, span SpanData) error {
	tenant := span.Tenant
	if fromContext, ok := core.TenantFrom(ctx); ok {
		tenant = string(fromContext)
	}
	attrs, err := json.Marshal(span.Attributes)
	if err != nil {
		return core.NewError(core.CodeInvalidArgument, "telemetry: span attributes are not serializable", nil)
	}
	_, err = s.store.Execer().Exec(ctx,
		`INSERT INTO telemetry_spans
		 (tenant_id, run_id, span_id, trace_id, parent_span_id, name, start_at, end_at, status, attrs)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tenant, span.RunID, span.SpanID, span.TraceID, span.ParentSpanID, span.Name,
		psql.TruncateMicrosecond(span.Start).UnixMicro(),
		psql.TruncateMicrosecond(span.End).UnixMicro(),
		span.Status, string(attrs))
	return spanStoreError("append span", err)
}

// ByRun implements SpanStore.
func (s *SQLSpanStore) ByRun(ctx context.Context, runID string) ([]SpanData, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "telemetry: run not found", nil)
	}
	spanOrder, err := psql.TextOrderClause(s.store.Dialect(), "span_id")
	if err != nil {
		return nil, spanStoreError("read spans", err)
	}
	rows, err := s.store.Execer().Query(ctx,
		`SELECT trace_id, span_id, parent_span_id, name, start_at, end_at, status, run_id, tenant_id, attrs
		 FROM telemetry_spans WHERE tenant_id = ? AND run_id = ?
		 ORDER BY start_at ASC, `+spanOrder, string(tenant), runID)
	if err != nil {
		return nil, spanStoreError("read spans", err)
	}
	defer func() { _ = rows.Close() }()

	var spans []SpanData
	for rows.Next() {
		var (
			span      SpanData
			startAt   int64
			endAt     int64
			attrsJSON string
		)
		if err := rows.Scan(&span.TraceID, &span.SpanID, &span.ParentSpanID, &span.Name,
			&startAt, &endAt, &span.Status, &span.RunID, &span.Tenant, &attrsJSON); err != nil {
			return nil, spanStoreError("read spans", err)
		}
		span.Start = time.UnixMicro(startAt).UTC()
		span.End = time.UnixMicro(endAt).UTC()
		if attrsJSON != "" {
			if err := json.Unmarshal([]byte(attrsJSON), &span.Attributes); err != nil {
				return nil, core.NewError(core.CodeInternal, "telemetry: stored span attributes are corrupt", nil)
			}
		}
		spans = append(spans, span)
	}
	if err := rows.Err(); err != nil {
		return nil, spanStoreError("read spans", err)
	}
	if len(spans) > 0 {
		return spans, nil
	}

	// A run this tenant never recorded but another tenant did must not look
	// like an empty run (REQ-TELEM-013).
	var anyTenant int
	if err := s.store.Execer().QueryRow(ctx,
		`SELECT COUNT(*) FROM telemetry_spans WHERE run_id = ?`, runID).Scan(&anyTenant); err != nil {
		return nil, spanStoreError("read spans", err)
	}
	if anyTenant > 0 {
		return nil, core.NewError(core.CodeNotFound, "telemetry: run not found", nil)
	}
	return nil, nil
}

// Purge implements SpanStore.
func (s *SQLSpanStore) Purge(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-s.retention)
	res, err := s.store.Execer().Exec(ctx,
		`DELETE FROM telemetry_spans WHERE end_at < ?`,
		psql.TruncateMicrosecond(cutoff).UnixMicro())
	if err != nil {
		return 0, spanStoreError("purge spans", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, spanStoreError("purge spans", err)
	}
	return n, nil
}

// spanStoreError wraps a store failure at the telemetry boundary. Store errors
// are internal to this package's contract: the caller of record or read sees a
// stable code, not driver text.
func spanStoreError(op string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := core.NewError(core.CodeInternal, "telemetry: "+op, nil)
	wrapped.Cause = err
	return wrapped
}
