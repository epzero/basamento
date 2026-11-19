package eventlog

import (
	"context"
	"database/sql"
	"errors"

	"github.com/epzero/basamento/core"
)

// Read returns every durable event of the run after sequence after, in
// sequence order (REQ-STRM-020). A cursor at or above the last sequence returns
// no events (REQ-STRM-021). The tenant comes from ctx and never from an
// argument: with no tenant the read fails with invalid_argument
// (REQ-STRM-025), and a run of another tenant is not_found (REQ-STRM-022).
func (l *Log) Read(ctx context.Context, runID string, after uint64) ([]Event, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkRunID(runID); err != nil {
		return nil, err
	}
	if err := l.requireRun(ctx, string(tenant), runID); err != nil {
		return nil, err
	}
	rows, err := l.store.Execer().Query(ctx,
		`SELECT seq, type, v, data, created_at
		 FROM eventlog_events
		 WHERE tenant_id = ? AND run_id = ? AND seq > ?
		 ORDER BY seq`,
		string(tenant), runID, int64(after))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := []Event{}
	for rows.Next() {
		var seq int64
		var e Event
		var data string
		if err := rows.Scan(&seq, &e.Type, &e.V, &data, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.RunID = runID
		e.Seq = uint64(seq)
		e.Data = []byte(data)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// State returns the recorded state of the run, scoped to the tenant of ctx.
func (l *Log) State(ctx context.Context, runID string) (string, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return "", err
	}
	if err := checkRunID(runID); err != nil {
		return "", err
	}
	var state string
	if err := l.store.Execer().QueryRow(ctx,
		`SELECT state FROM eventlog_runs WHERE tenant_id = ? AND run_id = ?`,
		string(tenant), runID).Scan(&state); err != nil {
		return "", mapNoRun(err)
	}
	return state, nil
}

// Outbox returns the outbox entries of the run in sequence order, scoped to the
// tenant of ctx. It is the operational view the relay keeps, not a client API.
func (l *Log) Outbox(ctx context.Context, runID string) ([]OutboxEntry, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkRunID(runID); err != nil {
		return nil, err
	}
	if err := l.requireRun(ctx, string(tenant), runID); err != nil {
		return nil, err
	}
	rows, err := l.store.Execer().Query(ctx,
		`SELECT seq, relayed_at FROM eventlog_outbox
		 WHERE tenant_id = ? AND run_id = ? ORDER BY seq`,
		string(tenant), runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := []OutboxEntry{}
	for rows.Next() {
		var seq int64
		var relayedAt sql.NullTime
		if err := rows.Scan(&seq, &relayedAt); err != nil {
			return nil, err
		}
		e := OutboxEntry{Seq: uint64(seq), Relayed: relayedAt.Valid, RelayedAt: relayedAt.Time}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (l *Log) requireRun(ctx context.Context, tenant, runID string) error {
	var one int
	err := l.store.Execer().QueryRow(ctx,
		`SELECT 1 FROM eventlog_runs WHERE tenant_id = ? AND run_id = ?`,
		tenant, runID).Scan(&one)
	if err != nil {
		return mapNoRun(err)
	}
	return nil
}

func mapNoRun(err error) error {
	if errors.Is(err, sql.ErrNoRows) || core.CodeOf(err) == core.CodeNotFound {
		return core.NewError(core.CodeNotFound, "eventlog: run not found", nil)
	}
	return err
}
