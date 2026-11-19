package eventlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

// Fault points of the write path, in the order a committed change reaches
// them. Tests name them as they appear in specs/streaming/event-log.feature.
const (
	PointBeforeAnyEvent   = "before any event is written"
	PointStateChangeSaved = "the state change written"
	firstEventWritten     = "the first event written"
	secondEventWritten    = "the second event written"
	thirdAndLaterEventFmt = "event %d written"
)

// CreateRun records a new run in state. It fails with code conflict when the
// tenant already has that run.
func (l *Log) CreateRun(ctx context.Context, runID, state string) error {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return err
	}
	if err := checkRunID(runID); err != nil {
		return err
	}
	if err := checkState(state); err != nil {
		return err
	}
	now := psql.TruncateMicrosecond(l.clock.Now())
	_, err = l.store.Execer().Exec(ctx,
		`INSERT INTO eventlog_runs (tenant_id, run_id, state, updated_at) VALUES (?, ?, ?, ?)`,
		string(tenant), runID, state, now)
	return err
}

// AppendChange records a state change of a run together with every durable
// event it records, in one transaction (REQ-STRM-010 … 012). It assigns the
// run's next sequence number to each event, starting at 1 (REQ-STRM-008), and
// serialises concurrent appends on the run (REQ-STRM-009). It fails with code
// not_found when the tenant has no such run, and with invalid_argument without
// a tenant.
func (l *Log) AppendChange(ctx context.Context, runID, state string, events []NewEvent) ([]Event, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkRunID(runID); err != nil {
		return nil, err
	}
	if err := checkState(state); err != nil {
		return nil, err
	}
	normalized := make([]Event, len(events))
	for i, e := range events {
		typ, version, data, err := normalizeEvent(e)
		if err != nil {
			return nil, err
		}
		normalized[i] = Event{RunID: runID, Type: typ, V: version, Data: data}
	}

	var out []Event
	err = l.store.InTx(ctx, func(ctx context.Context, exec *psql.Execer) error {
		lock := `SELECT state FROM eventlog_runs WHERE tenant_id = ? AND run_id = ?`
		if l.store.Dialect() == "postgres" {
			lock += ` FOR UPDATE`
		}
		var current string
		if err := exec.QueryRow(ctx, lock, string(tenant), runID).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) || core.CodeOf(err) == core.CodeNotFound {
				return core.NewError(core.CodeNotFound, "eventlog: run not found", nil)
			}
			return err
		}
		var last int64
		if err := exec.QueryRow(ctx,
			`SELECT COALESCE(MAX(seq), 0) FROM eventlog_events WHERE tenant_id = ? AND run_id = ?`,
			string(tenant), runID).Scan(&last); err != nil {
			return err
		}
		if err := l.fire(PointBeforeAnyEvent); err != nil {
			return err
		}
		now := psql.TruncateMicrosecond(l.clock.Now())
		out = make([]Event, 0, len(normalized))
		for i, e := range normalized {
			e.Seq = uint64(last) + uint64(i) + 1
			e.CreatedAt = now
			if _, err := exec.Exec(ctx,
				`INSERT INTO eventlog_events (tenant_id, run_id, seq, type, v, data, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				string(tenant), runID, int64(e.Seq), e.Type, e.V, string(e.Data), now); err != nil {
				return err
			}
			if err := l.fire(eventWrittenPoint(i + 1)); err != nil {
				return err
			}
			if _, err := exec.Exec(ctx,
				`INSERT INTO eventlog_outbox (tenant_id, run_id, seq) VALUES (?, ?, ?)`,
				string(tenant), runID, int64(e.Seq)); err != nil {
				return err
			}
			out = append(out, e)
		}
		if _, err := exec.Exec(ctx,
			`UPDATE eventlog_runs SET state = ?, updated_at = ? WHERE tenant_id = ? AND run_id = ?`,
			state, now, string(tenant), runID); err != nil {
			return err
		}
		return l.fire(PointStateChangeSaved)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func eventWrittenPoint(n int) string {
	switch n {
	case 1:
		return firstEventWritten
	case 2:
		return secondEventWritten
	default:
		return fmt.Sprintf(thirdAndLaterEventFmt, n)
	}
}
