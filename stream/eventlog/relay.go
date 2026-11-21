package eventlog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

// DefaultRelayStep is the wait between relay passes when the bus is reachable
// and nothing is pending. It is only the idle step; tests drive Run directly
// or advance the fake clock.
const DefaultRelayStep = time.Second

// Envelope is what the relay publishes for one durable event (REQ-STRM-024).
// The type travels here, not in the subject; consumers order and deduplicate by
// Seq (REQ-STRM-016).
type Envelope struct {
	RunID string          `json:"run_id"`
	Seq   uint64          `json:"seq"`
	Type  string          `json:"type"`
	V     int             `json:"v"`
	Data  json.RawMessage `json:"data"`
}

// RelayOptions configures a Relay.
type RelayOptions struct {
	// Clock supplies the relayed timestamp.
	Clock core.Clock
	// Fault is the test-only seam described on Fault; nil in production.
	Fault Fault
	// Step is the idle wait of Loop; zero means DefaultRelayStep.
	Step time.Duration
	// After returns a channel that fires after d. Nil means time.After. Tests
	// inject the fake clock so a loop pass runs without sleeping.
	After func(d time.Duration) <-chan time.Time
}

// Relay publishes every unrelayed outbox entry to the bus in sequence order and
// marks it relayed only after the bus accepted it (REQ-STRM-013 … 019).
type Relay struct {
	log   *Log
	bus   platform.Bus
	clock core.Clock
	fault Fault
	step  time.Duration
	after func(time.Duration) <-chan time.Time
}

// NewRelay builds a relay over log and bus.
func NewRelay(log *Log, bus platform.Bus, opts RelayOptions) (*Relay, error) {
	if log == nil || bus == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "eventlog: relay needs a log and a bus", nil)
	}
	clock := opts.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	step := opts.Step
	if step <= 0 {
		step = DefaultRelayStep
	}
	after := opts.After
	if after == nil {
		after = time.After
	}
	return &Relay{log: log, bus: bus, clock: clock, fault: opts.Fault, step: step, after: after}, nil
}

type pendingRun struct {
	tenant string
	runID  string
}

// Run makes one relay pass. Passes over the same log are serialised by relayMu,
// and each run is relayed under a lease in the store, so passes over another Log
// or another process cannot publish the same run's pending rows at once
// (REQ-STRM-013). It publishes every run's
// unrelayed entries in sequence order. A bus that cannot be reached ends the
// pass with no error and leaves the head entry unrelayed, so a later pass
// resumes exactly there (REQ-STRM-017 … 019). Entries are marked relayed only
// after the bus accepted them (REQ-STRM-014); a stop between the two publishes
// the entry again after a restart (REQ-STRM-015). An error in one run is
// contained: the pass continues with the other runs and returns the first
// error, so Loop retries it.
func (r *Relay) Run(ctx context.Context) error {
	r.log.relayMu.Lock()
	defer r.log.relayMu.Unlock()

	runs, err := r.pendingRuns(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, run := range runs {
		if ctx.Err() != nil {
			break
		}
		subject, err := RunSubject(run.tenant, run.runID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := r.relayRunLeased(ctx, run, subject); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Loop runs a pass, then waits Step on the injected clock and runs again,
// until ctx is cancelled. A failed pass does not end the loop: the error is
// retried after the next Step.
func (r *Relay) Loop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = r.Run(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-r.after(r.step):
		}
	}
}

func (r *Relay) pendingRuns(ctx context.Context) ([]pendingRun, error) {
	rows, err := r.log.store.Execer().Query(ctx,
		`SELECT DISTINCT tenant_id, run_id FROM eventlog_outbox
		 WHERE relayed_at IS NULL ORDER BY tenant_id, run_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var runs []pendingRun
	for rows.Next() {
		var p pendingRun
		if err := rows.Scan(&p.tenant, &p.runID); err != nil {
			return nil, err
		}
		runs = append(runs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

// relayLeaseFor is how long a pass owns a run before another pass may take it
// over. It must exceed the time one pass needs to publish a run's pending
// entries, including a slow bus. A pass that stops without releasing its lease
// (a crashed process) leaves it to expire, and the next pass takes it and
// republishes the unmarked head entry (REQ-STRM-015).
const relayLeaseFor = time.Minute

// relayRunLeased relays one run while holding its lease in the shared store.
// relayMu only serialises passes over this Log; the lease is what keeps another
// Log or process from publishing the same run at the same time.
func (r *Relay) relayRunLeased(ctx context.Context, run pendingRun, subject string) error {
	until, held, err := r.acquireRunLease(ctx, run)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	defer r.releaseRunLease(ctx, run, until)
	return r.relayRun(ctx, run, subject)
}

// acquireRunLease takes the run's lease until now+relayLeaseFor, or reports
// false when another pass still holds it. The statement is atomic on SQLite and
// Postgres, so exactly one concurrent pass wins; no transaction is needed.
func (r *Relay) acquireRunLease(ctx context.Context, run pendingRun) (time.Time, bool, error) {
	now := psql.TruncateMicrosecond(r.clock.Now())
	until := now.Add(relayLeaseFor)
	var tenant string
	err := r.log.store.Execer().QueryRow(ctx,
		`INSERT INTO eventlog_relay_leases (tenant_id, run_id, expires_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT (tenant_id, run_id) DO UPDATE
		   SET expires_at = excluded.expires_at
		 WHERE eventlog_relay_leases.expires_at <= ?
		 RETURNING tenant_id`,
		run.tenant, run.runID, until, now).Scan(&tenant)
	if err != nil {
		// A held lease makes the ON CONFLICT WHERE reject the update, so the
		// RETURNING yields no row and Scan maps it to not_found.
		if core.CodeOf(err) == core.CodeNotFound {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return until, true, nil
}

// releaseRunLease drops the lease so the next pass may relay the run at once
// instead of after expiry. A failed release is harmless: the lease expires, and
// the next pass then republishes what was not marked.
func (r *Relay) releaseRunLease(ctx context.Context, run pendingRun, until time.Time) {
	_, _ = r.log.store.Execer().Exec(ctx,
		`DELETE FROM eventlog_relay_leases
		 WHERE tenant_id = ? AND run_id = ? AND expires_at = ?`,
		run.tenant, run.runID, until)
}

func (r *Relay) relayRun(ctx context.Context, run pendingRun, subject string) error {
	tenantCtx := core.WithTenant(ctx, core.TenantID(run.tenant))
	for {
		entries, err := r.pendingEntries(tenantCtx, run.runID)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		for _, e := range entries {
			env := Envelope{RunID: e.RunID, Seq: e.Seq, Type: e.Type, V: e.V, Data: e.Data}
			payload, err := json.Marshal(env)
			if err != nil {
				return core.NewError(core.CodeInternal, "eventlog: encode envelope", nil)
			}
			if _, err := r.bus.Publish(ctx, subject, payload); err != nil {
				if core.CodeOf(err) == core.CodeUnavailable {
					return nil
				}
				return err
			}
			if err := r.fire(fmt.Sprintf("after sending entry %d, before marking", e.Seq)); err != nil {
				return nil
			}
			if err := r.fire(fmt.Sprintf("while marking entry %d, before saving", e.Seq)); err != nil {
				return nil
			}
			now := psql.TruncateMicrosecond(r.clock.Now())
			if _, err := r.log.store.Execer().Exec(ctx,
				`UPDATE eventlog_outbox SET relayed_at = ?
				 WHERE tenant_id = ? AND run_id = ? AND seq = ? AND relayed_at IS NULL`,
				now, run.tenant, run.runID, int64(e.Seq)); err != nil {
				return err
			}
		}
	}
}

func (r *Relay) pendingEntries(ctx context.Context, runID string) ([]Event, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, core.NewError(core.CodeInvalidArgument, "eventlog: tenant required", nil)
	}
	rows, err := r.log.store.Execer().Query(ctx,
		`SELECT e.seq, e.type, e.v, e.data, e.created_at
		 FROM eventlog_outbox o
		 JOIN eventlog_events e
		   ON e.tenant_id = o.tenant_id AND e.run_id = o.run_id AND e.seq = o.seq
		 WHERE o.tenant_id = ? AND o.run_id = ? AND o.relayed_at IS NULL
		 ORDER BY o.seq`,
		string(tenant), runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var events []Event
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

func (r *Relay) fire(point string) error {
	if r.fault == nil {
		return nil
	}
	return r.fault(point)
}
