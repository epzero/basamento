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
	// Purge deletes the spans that ended before before and returns the count.
	Purge(ctx context.Context, before time.Time) (int64, error)
}

// SQLSpanStore is the SpanStore over a platform/sql store. The same schema is
// created idempotently for SQLite and Postgres.
type SQLSpanStore struct {
	store *psql.Store
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

// NewSQLSpanStore returns a span store on store and creates its table when it
// is missing. The caller owns store.
func NewSQLSpanStore(ctx context.Context, store *psql.Store) (*SQLSpanStore, error) {
	if store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: nil SQL store", nil)
	}
	if _, err := store.Execer().Exec(ctx, spanSchema); err != nil {
		return nil, err
	}
	if _, err := store.Execer().Exec(ctx, spanIndex); err != nil {
		return nil, err
	}
	return &SQLSpanStore{store: store}, nil
}

// Append implements SpanStore.
func (s *SQLSpanStore) Append(ctx context.Context, span SpanData) error {
	attrs, err := json.Marshal(span.Attributes)
	if err != nil {
		return core.NewError(core.CodeInvalidArgument, "telemetry: span attributes are not serializable", nil)
	}
	_, err = s.store.Execer().Exec(ctx,
		`INSERT INTO telemetry_spans
		 (tenant_id, run_id, span_id, trace_id, parent_span_id, name, start_at, end_at, status, attrs)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		span.Tenant, span.RunID, span.SpanID, span.TraceID, span.ParentSpanID, span.Name,
		psql.TruncateMicrosecond(span.Start).UnixMicro(),
		psql.TruncateMicrosecond(span.End).UnixMicro(),
		span.Status, string(attrs))
	return err
}

// ByRun implements SpanStore.
func (s *SQLSpanStore) ByRun(ctx context.Context, runID string) ([]SpanData, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "telemetry: run not found", nil)
	}
	rows, err := s.store.Execer().Query(ctx,
		`SELECT trace_id, span_id, parent_span_id, name, start_at, end_at, status, run_id, tenant_id, attrs
		 FROM telemetry_spans WHERE tenant_id = ? AND run_id = ?
		 ORDER BY start_at ASC, span_id ASC`, string(tenant), runID)
	if err != nil {
		return nil, err
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
			return nil, err
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
		return nil, err
	}
	if len(spans) > 0 {
		return spans, nil
	}

	// A run this tenant never recorded but another tenant did must not look
	// like an empty run (REQ-TELEM-013).
	var anyTenant int
	if err := s.store.Execer().QueryRow(ctx,
		`SELECT COUNT(*) FROM telemetry_spans WHERE run_id = ?`, runID).Scan(&anyTenant); err != nil {
		return nil, err
	}
	if anyTenant > 0 {
		return nil, core.NewError(core.CodeNotFound, "telemetry: run not found", nil)
	}
	return nil, nil
}

// Purge implements SpanStore.
func (s *SQLSpanStore) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.store.Execer().Exec(ctx,
		`DELETE FROM telemetry_spans WHERE end_at < ?`,
		psql.TruncateMicrosecond(before).UnixMicro())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
