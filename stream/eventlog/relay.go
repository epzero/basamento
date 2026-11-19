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

// Run makes one relay pass. It publishes every run's unrelayed entries in
// sequence order. A bus that cannot be reached ends the pass with no error and
// leaves the head entry unrelayed, so a later pass resumes exactly there
// (REQ-STRM-017 … 019). Entries are marked relayed only after the bus accepted
// them (REQ-STRM-014); a stop between the two publishes the entry again after a
// restart (REQ-STRM-015).
func (r *Relay) Run(ctx context.Context) error {
	runs, err := r.pendingRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		subject, err := RunSubject(run.runID)
		if err != nil {
			return err
		}
		if err := r.relayRun(ctx, run, subject); err != nil {
			return err
		}
	}
	return nil
}

// Loop runs a pass, then waits Step on the injected clock and runs again,
// until ctx is cancelled.
func (r *Relay) Loop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := r.Run(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
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
