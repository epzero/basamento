package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

// invocation states. A row leaves waiting only for leased, and leaves leased
// only for a terminal state (ADR-0012 §3, §7).
const (
	stateWaiting   = "waiting"
	stateLeased    = "leased"
	stateSucceeded = "succeeded"
	stateFailed    = "failed"
	stateCancelled = "cancelled"
	stateRemote    = "remote"
)

// Enqueue adds one invocation to a pool's queue. The queue has one row per
// client ref, so a repeated enqueue of the same client ref returns the row it
// already has and Start stays idempotent (ADR-0012 §7).
func (s *Store) Enqueue(ctx context.Context, inv Invocation) (Invocation, error) {
	if inv.ClientRef == "" {
		return Invocation{}, core.NewError(core.CodeInvalidArgument, "worker: invocation has no client ref", nil)
	}
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return Invocation{}, err
	}
	class, err := ParseClass(string(inv.Class))
	if err != nil {
		return Invocation{}, err
	}
	inv.Class = class
	if inv.Units <= 0 {
		inv.Units = 1
	}
	if pool.Tenant != "" {
		if inv.Tenant != "" && inv.Tenant != pool.Tenant {
			return Invocation{}, core.NewError(core.CodePermissionDenied,
				"a tenant's pool takes only that tenant's work", map[string]any{"pool": pool.ID})
		}
		inv.Tenant = pool.Tenant
	}
	if inv.QueueTimeout < 0 {
		return Invocation{}, core.NewError(core.CodeInvalidArgument, "worker: queue timeout must not be negative", nil)
	}
	if existing, err := s.Invocation(ctx, inv.ClientRef); err == nil {
		return existing, nil
	} else if core.CodeOf(err) != core.CodeNotFound {
		return Invocation{}, err
	}
	now := s.now()
	inv.QueuedAt = now
	inv.State = stateWaiting
	req := inv.Request
	if len(req) == 0 {
		req = json.RawMessage("{}")
	}
	_, err = s.db.Execer().Exec(ctx,
		`INSERT INTO worker_invocations
		 (client_ref, pool_id, tenant, model, class, units, state, worker_id, attempt, started, in_doubt,
		  queued_unix_ms, lease_expires_unix_ms, in_doubt_until_unix_ms, queue_timeout_ms, max_runtime_ms,
		  cancel_requested, reserved_mpts, charged_mpts, code, request, result, provider_handle, spilled, trace_parent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', 0, 0, 0, ?, 0, 0, ?, ?, 0, ?, 0, '', ?, '', '', 0, ?)`,
		inv.ClientRef, inv.Pool, string(inv.Tenant), inv.Model, string(inv.Class), inv.Units, stateWaiting,
		unixms(now), inv.QueueTimeout.Milliseconds(), inv.MaxRuntime.Milliseconds(),
		inv.ReservedMpts, string(req), inv.TraceParent)
	if err != nil {
		return Invocation{}, err
	}
	return s.Invocation(ctx, inv.ClientRef)
}

// Invocation reads one invocation by client ref.
func (s *Store) Invocation(ctx context.Context, clientRef string) (Invocation, error) {
	return scanInvocation(s.db.Execer().QueryRow(ctx, selectInvocation+` WHERE client_ref = ?`, clientRef))
}

const selectInvocation = `SELECT client_ref, pool_id, tenant, model, class, units, state, worker_id, attempt,
	started, in_doubt, queued_unix_ms, lease_expires_unix_ms, in_doubt_until_unix_ms, queue_timeout_ms,
	max_runtime_ms, cancel_requested, reserved_mpts, charged_mpts, code, request, result, provider_handle,
	spilled, trace_parent FROM worker_invocations`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInvocation(row rowScanner) (Invocation, error) {
	var (
		inv                                   Invocation
		tenant, class, state, code            string
		started, inDoubt, cancel              int
		queued, lease, doubt, timeout, maxRun int64
		request, result                       string
		spilled                               int
	)
	err := row.Scan(&inv.ClientRef, &inv.Pool, &tenant, &inv.Model, &class, &inv.Units, &state, &inv.WorkerID,
		&inv.Attempt, &started, &inDoubt, &queued, &lease, &doubt, &timeout, &maxRun, &cancel,
		&inv.ReservedMpts, &inv.ChargedMpts, &code, &request, &result, &inv.ProviderHandle, &spilled, &inv.TraceParent)
	if core.CodeOf(err) == core.CodeNotFound {
		return Invocation{}, core.NewError(core.CodeNotFound, "invocation does not exist", nil)
	}
	if err != nil {
		return Invocation{}, err
	}
	inv.Tenant = core.TenantID(tenant)
	inv.Class = Class(class)
	inv.State = state
	inv.Started = started != 0
	inv.InDoubt = inDoubt != 0
	inv.CancelRequested = cancel != 0
	inv.QueuedAt = fromms(queued)
	inv.LeaseExpires = fromms(lease)
	inv.InDoubtUntil = fromms(doubt)
	inv.QueueTimeout = time.Duration(timeout) * time.Millisecond
	inv.MaxRuntime = time.Duration(maxRun) * time.Millisecond
	inv.Code = core.Code(code)
	inv.Request = json.RawMessage(request)
	inv.Result = json.RawMessage(result)
	if len(inv.Request) == 0 {
		inv.Request = json.RawMessage("{}")
	}
	return inv, nil
}

// validWorker returns the worker behind a credential and refuses a worker
// that no longer exists or was removed (REQ-MODEL-030).
func (s *Store) validWorker(ctx context.Context, workerID, credential string) (Worker, error) {
	return s.authenticate(ctx, workerID, credential)
}

// Lease asks the platform for work. The platform picks the invocations in its
// own order; the worker only says how much it has free (ADR-0012 §5).
func (s *Store) Lease(ctx context.Context, req LeaseRequest) ([]Invocation, error) {
	w, err := s.validWorker(ctx, req.WorkerID, req.Credential)
	if err != nil {
		return nil, err
	}
	if err := s.sweep(ctx); err != nil {
		return nil, err
	}
	if w.State != StateActive {
		return nil, nil
	}
	pool, err := s.Pool(ctx, w.Pool)
	if err != nil {
		return nil, err
	}
	if req.FreeUnits <= 0 {
		return nil, nil
	}
	var leased []Invocation
	err = s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		used, err := unitsInUse(ctx, tx, w.ID)
		if err != nil {
			return err
		}
		free := w.Capacity - used
		if req.FreeUnits < free {
			free = req.FreeUnits
		}
		if free <= 0 {
			return nil
		}
		models := map[string]Descriptor{}
		for _, d := range w.Descriptors {
			models[d.ID] = d
		}
		candidates, err := s.waitingCandidates(ctx, tx, w.Pool, models)
		if err != nil {
			return err
		}
		leasedCounts, err := leasedByTenant(ctx, tx, w.Pool)
		if err != nil {
			return err
		}
		chosen, err := choose(candidates, pool, s.now(), models, free, req.TimeLeft,
			leasedCounts, func(t core.TenantID) TenantSettings {
				ts, err := tenantSettingsTx(ctx, tx, w.Pool, t)
				if err != nil {
					return TenantSettings{Weight: 1}
				}
				return ts
			})
		if err != nil {
			return err
		}
		for _, inv := range chosen {
			expires := s.now().Add(pool.Lease)
			if _, err := tx.Exec(ctx,
				`UPDATE worker_invocations SET state = ?, worker_id = ?, attempt = attempt + 1,
					started = 0, in_doubt = 0, lease_expires_unix_ms = ?, in_doubt_until_unix_ms = 0
				 WHERE client_ref = ? AND state = ?`,
				stateLeased, w.ID, unixms(expires), inv.ClientRef, stateWaiting); err != nil {
				return err
			}
			inv.State = stateLeased
			inv.WorkerID = w.ID
			inv.Attempt++
			inv.Started = false
			inv.InDoubt = false
			inv.LeaseExpires = expires
			leased = append(leased, inv)
			free -= inv.Units
			if free <= 0 {
				break
			}
		}
		return setUnitsInUse(ctx, tx, w.ID)
	})
	if err != nil {
		return nil, err
	}
	return leased, nil
}

// waitingCandidates lists the pool's waiting invocations this worker can serve,
// oldest first, before ordering.
func (s *Store) waitingCandidates(ctx context.Context, exec *psql.Execer, poolID string, models map[string]Descriptor) ([]Invocation, error) {
	rows, err := exec.Query(ctx, selectInvocation+` WHERE pool_id = ? AND state = ? ORDER BY queued_unix_ms, client_ref`, poolID, stateWaiting)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Invocation
	for rows.Next() {
		inv, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		if _, ok := models[inv.Model]; !ok {
			continue
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func unitsInUse(ctx context.Context, exec *psql.Execer, workerID string) (int, error) {
	var used int
	err := exec.QueryRow(ctx,
		`SELECT COALESCE(SUM(units), 0) FROM worker_invocations WHERE worker_id = ? AND state = ?`,
		workerID, stateLeased).Scan(&used)
	return used, err
}

func setUnitsInUse(ctx context.Context, exec *psql.Execer, workerID string) error {
	used, err := unitsInUse(ctx, exec, workerID)
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, `UPDATE worker_workers SET units_used = ? WHERE worker_id = ?`, used, workerID)
	return err
}

func leasedByTenant(ctx context.Context, exec *psql.Execer, poolID string) (map[core.TenantID]int, error) {
	rows, err := exec.Query(ctx,
		`SELECT tenant, COUNT(*) FROM worker_invocations WHERE pool_id = ? AND state = ? GROUP BY tenant`,
		poolID, stateLeased)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[core.TenantID]int{}
	for rows.Next() {
		var tenant string
		var n int
		if err := rows.Scan(&tenant, &n); err != nil {
			return nil, err
		}
		out[core.TenantID(tenant)] = n
	}
	return out, rows.Err()
}

func tenantSettingsTx(ctx context.Context, exec *psql.Execer, poolID string, tenant core.TenantID) (TenantSettings, error) {
	ts := TenantSettings{Weight: 1}
	var spill int
	var after int64
	err := exec.QueryRow(ctx,
		`SELECT weight, concurrency, spill_over, spill_after_ms FROM worker_tenants
		 WHERE pool_id = ? AND tenant = ?`, poolID, string(tenant)).
		Scan(&ts.Weight, &ts.Concurrency, &spill, &after)
	if core.CodeOf(err) == core.CodeNotFound {
		return ts, nil
	}
	if err != nil {
		return ts, err
	}
	ts.SpillOver = spill != 0
	ts.SpillAfter = time.Duration(after) * time.Millisecond
	return ts, nil
}

// choose orders candidates: effective class (ageing moves one up), then
// fairness in the shared pool, then age (REQ-MODEL-015 to REQ-MODEL-019).
func choose(candidates []Invocation, pool PoolSpec, now time.Time, models map[string]Descriptor,
	free int, timeLeft time.Duration, leased map[core.TenantID]int,
	settings func(core.TenantID) TenantSettings) ([]Invocation, error) {

	type ranked struct {
		inv   Invocation
		rank  int
		ratio float64
		when  time.Time
	}
	rankedList := make([]ranked, 0, len(candidates))
	for _, inv := range candidates {
		settingsFor := settings(inv.Tenant)
		if pool.Tenant == "" && settingsFor.Concurrency > 0 && leased[inv.Tenant] >= settingsFor.Concurrency {
			continue
		}
		rank := inv.Class.rank()
		if pool.Ageing > 0 && now.Sub(inv.QueuedAt) >= pool.Ageing && rank > 0 {
			rank--
		}
		weight := settingsFor.Weight
		if weight <= 0 {
			weight = 1
		}
		rankedList = append(rankedList, ranked{
			inv:   inv,
			rank:  rank,
			ratio: float64(leased[inv.Tenant]) / float64(weight),
			when:  inv.QueuedAt,
		})
	}
	sort.SliceStable(rankedList, func(i, j int) bool {
		a, b := rankedList[i], rankedList[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if pool.Tenant == "" && a.ratio != b.ratio {
			return a.ratio < b.ratio
		}
		if !a.when.Equal(b.when) {
			return a.when.Before(b.when)
		}
		return a.inv.ClientRef < b.inv.ClientRef
	})
	var out []Invocation
	remaining := free
	for _, r := range rankedList {
		if r.inv.Units > remaining {
			continue
		}
		if r.inv.MaxRuntime > 0 && timeLeft > 0 && r.inv.MaxRuntime > timeLeft {
			continue
		}
		out = append(out, r.inv)
		remaining -= r.inv.Units
		if remaining <= 0 {
			break
		}
	}
	return out, nil
}

// Starting records that a worker is about to call its provider. An invocation
// whose lease expired is recovered to its own worker only inside the grace
// period (REQ-MODEL-021, REQ-MODEL-023).
func (s *Store) Starting(ctx context.Context, workerID, credential, clientRef string) error {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return err
	}
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return err
	}
	if isTerminal(inv.State) || inv.State != stateLeased || inv.WorkerID != w.ID {
		return errLeaseLost(clientRef)
	}
	_, err = s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET started = 1, in_doubt = 0, in_doubt_until_unix_ms = 0 WHERE client_ref = ?`,
		clientRef)
	return err
}

// Started records the provider's handle after a start.
func (s *Store) Started(ctx context.Context, workerID, credential, clientRef, providerHandle string) error {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return err
	}
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return err
	}
	if isTerminal(inv.State) || inv.State != stateLeased || inv.WorkerID != w.ID {
		return errLeaseLost(clientRef)
	}
	_, err = s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET started = 1, in_doubt = 0, in_doubt_until_unix_ms = 0, provider_handle = ?
		 WHERE client_ref = ?`, providerHandle, clientRef)
	return err
}

// Resume lets a worker take back an in-doubt invocation inside the grace
// period (REQ-MODEL-023).
func (s *Store) Resume(ctx context.Context, workerID, credential, clientRef string) error {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return err
	}
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return err
	}
	if isTerminal(inv.State) || inv.State != stateLeased || inv.WorkerID != w.ID || !inv.InDoubt {
		return errLeaseLost(clientRef)
	}
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return err
	}
	_, err = s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET in_doubt = 0, in_doubt_until_unix_ms = 0, lease_expires_unix_ms = ?
		 WHERE client_ref = ?`, unixms(s.now().Add(pool.Lease)), clientRef)
	return err
}

// Event is one piece of partial output a worker reports.
type Event struct {
	Seq      int64           `json:"seq"`
	Kind     string          `json:"kind"`
	Text     string          `json:"text,omitempty"`
	Audio    []byte          `json:"audio,omitempty"`
	Progress float64         `json:"progress,omitempty"`
	Attempt  int             `json:"attempt,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Events appends partial output for a lease.
func (s *Store) Events(ctx context.Context, workerID, credential, clientRef string, events []Event) error {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return err
	}
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return err
	}
	if isTerminal(inv.State) || inv.State != stateLeased || inv.WorkerID != w.ID {
		return errLeaseLost(clientRef)
	}
	return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		if inv.InDoubt {
			if _, err := tx.Exec(ctx,
				`UPDATE worker_invocations SET in_doubt = 0, in_doubt_until_unix_ms = 0 WHERE client_ref = ?`,
				clientRef); err != nil {
				return err
			}
		}
		for _, e := range events {
			data := e.Data
			if len(data) == 0 {
				data = json.RawMessage("{}")
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO worker_events (client_ref, seq, kind, data) VALUES (?, ?, ?, ?)`,
				clientRef, e.Seq, e.Kind, string(data)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Finish reports the result of a lease. A report on an invocation that has
// already ended is refused with lease_lost and the recorded result stands
// (REQ-MODEL-024).
func (s *Store) Finish(ctx context.Context, workerID, credential, clientRef, state string, result json.RawMessage, code core.Code, cost int64) error {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return err
	}
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return err
	}
	if isTerminal(inv.State) {
		return errLeaseLost(clientRef)
	}
	if inv.State != stateLeased || inv.WorkerID != w.ID {
		return errLeaseLost(clientRef)
	}
	switch state {
	case stateSucceeded, stateFailed, stateCancelled:
	default:
		return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("unknown result state %q", state), nil)
	}
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	err = s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		if _, err := tx.Exec(ctx,
			`UPDATE worker_invocations SET state = ?, charged_mpts = ?, code = ?, result = ?,
				in_doubt = 0, in_doubt_until_unix_ms = 0, cancel_requested = 0 WHERE client_ref = ?`,
			state, cost, string(code), string(result), clientRef); err != nil {
			return err
		}
		return setUnitsInUse(ctx, tx, w.ID)
	})
	return err
}

// Release gives a lease back unused, or not before a time (the vendor's rate
// limit).
func (s *Store) Release(ctx context.Context, workerID, credential, clientRef string, notBefore time.Time) error {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return err
	}
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return err
	}
	if isTerminal(inv.State) || inv.State != stateLeased || inv.WorkerID != w.ID {
		return errLeaseLost(clientRef)
	}
	if inv.Started {
		// A started, non-restart-safe invocation stays in doubt for its own
		// worker; a restart-safe one goes back to the queue.
		d, err := s.descriptorFor(ctx, inv.Pool, inv.Model)
		if err != nil {
			return err
		}
		if !d.RestartSafe {
			return s.markInDoubt(ctx, inv)
		}
	}
	queued := s.now()
	if !notBefore.IsZero() {
		queued = notBefore.UTC()
	}
	return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		if _, err := tx.Exec(ctx,
			`UPDATE worker_invocations SET state = ?, worker_id = '', started = 0, in_doubt = 0,
				lease_expires_unix_ms = 0, in_doubt_until_unix_ms = 0, queued_unix_ms = ?
			 WHERE client_ref = ?`, stateWaiting, unixms(queued), clientRef); err != nil {
			return err
		}
		return setUnitsInUse(ctx, tx, w.ID)
	})
}

// Heartbeat keeps the worker present and its leases alive and carries what the
// platform wants: cancellations, a drain, a new expiry (ADR-0012 §4, §7).
func (s *Store) Heartbeat(ctx context.Context, workerID, credential string, freeUnits int) (HeartbeatAnswer, error) {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	if err := s.sweep(ctx); err != nil {
		return HeartbeatAnswer{}, err
	}
	pool, err := s.Pool(ctx, w.Pool)
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	expires := s.now().Add(pool.Lease)
	err = s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		if _, err := tx.Exec(ctx,
			`UPDATE worker_invocations SET lease_expires_unix_ms = ?
			 WHERE worker_id = ? AND state = ? AND in_doubt = 0`,
			unixms(expires), w.ID, stateLeased); err != nil {
			return err
		}
		if freeUnits >= 0 {
			if _, err := tx.Exec(ctx, `UPDATE worker_workers SET units_used = ? WHERE worker_id = ?`, freeUnits, w.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	rows, err := s.db.Execer().Query(ctx,
		`SELECT client_ref FROM worker_invocations WHERE worker_id = ? AND state = ? AND cancel_requested = 1 ORDER BY client_ref`,
		w.ID, stateLeased)
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	defer func() { _ = rows.Close() }()
	answer := HeartbeatAnswer{Drain: w.State == StateDraining, LeaseExpires: expires, State: w.State}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return HeartbeatAnswer{}, err
		}
		answer.Cancel = append(answer.Cancel, ref)
	}
	return answer, rows.Err()
}

// Cancel removes a waiting invocation, or reaches a leased one in the next
// heartbeat answer (REQ-MODEL-026).
func (s *Store) Cancel(ctx context.Context, clientRef string) error {
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return err
	}
	if isTerminal(inv.State) {
		return nil
	}
	if inv.State == stateWaiting {
		_, err = s.db.Execer().Exec(ctx,
			`UPDATE worker_invocations SET state = ?, charged_mpts = 0, code = ? WHERE client_ref = ?`,
			stateCancelled, string(core.CodeCancelled), clientRef)
		return err
	}
	_, err = s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET cancel_requested = 1 WHERE client_ref = ?`, clientRef)
	return err
}

// Recover extends every lease by one period when the platform starts, so an
// outage of its own does not turn into lost work (REQ-MODEL-027).
func (s *Store) Recover(ctx context.Context) error {
	pools, err := s.ListPools(ctx)
	if err != nil {
		return err
	}
	specs := make([]PoolSpec, 0, len(pools))
	for _, id := range pools {
		pool, err := s.Pool(ctx, id)
		if err != nil {
			return err
		}
		specs = append(specs, pool)
	}
	return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		restart := unixms(s.now())
		for _, pool := range specs {
			period := pool.Lease.Milliseconds()
			// Extend from the restart: an outage of the platform must not
			// turn into lost work, whatever it lasted (REQ-MODEL-027).
			if _, err := tx.Exec(ctx,
				`UPDATE worker_invocations SET lease_expires_unix_ms = ?
				 WHERE pool_id = ? AND state = ? AND lease_expires_unix_ms > 0`,
				restart+period, pool.ID, stateLeased); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE worker_invocations SET in_doubt_until_unix_ms = ?
				 WHERE pool_id = ? AND state = ? AND in_doubt_until_unix_ms > 0`,
				restart+period, pool.ID, stateLeased); err != nil {
				return err
			}
		}
		return nil
	})
}

// Sweep applies the queue timeout, expired leases, restart-safe redelivery and
// the third attempt's worker_lost (REQ-MODEL-014, -021, -022, -025).
func (s *Store) Sweep(ctx context.Context) error { return s.sweep(ctx) }

func (s *Store) sweep(ctx context.Context) error {
	return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		now := s.now()
		rows, err := tx.Query(ctx, selectInvocation+` WHERE state IN (?, ?)`, stateWaiting, stateLeased)
		if err != nil {
			return err
		}
		var all []Invocation
		for rows.Next() {
			inv, err := scanInvocation(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			all = append(all, inv)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		touched := map[string]bool{}
		for _, inv := range all {
			switch {
			case inv.State == stateWaiting:
				if inv.QueueTimeout > 0 && !now.Before(inv.QueuedAt.Add(inv.QueueTimeout)) {
					if err := failRow(ctx, tx, inv.ClientRef, core.CodeUnavailable, 0); err != nil {
						return err
					}
					touched[inv.Pool] = true
				}
			case inv.InDoubt:
				if !inv.InDoubtUntil.IsZero() && !now.Before(inv.InDoubtUntil) {
					if err := failRow(ctx, tx, inv.ClientRef, CodeWorkerLost, inv.ReservedMpts); err != nil {
						return err
					}
					touched[inv.Pool] = true
				}
			case !inv.LeaseExpires.IsZero() && !now.Before(inv.LeaseExpires):
				if inv.Attempt >= 3 {
					if err := failRow(ctx, tx, inv.ClientRef, CodeWorkerLost, inv.ReservedMpts); err != nil {
						return err
					}
					touched[inv.Pool] = true
					continue
				}
				if !inv.Started {
					if _, err := tx.Exec(ctx,
						`UPDATE worker_invocations SET state = ?, worker_id = '', lease_expires_unix_ms = 0,
							in_doubt = 0, in_doubt_until_unix_ms = 0 WHERE client_ref = ?`,
						stateWaiting, inv.ClientRef); err != nil {
						return err
					}
					touched[inv.Pool] = true
					continue
				}
				d, err := descriptorForTx(ctx, tx, inv.Pool, inv.Model)
				if err != nil {
					return err
				}
				if d.RestartSafe {
					if _, err := tx.Exec(ctx,
						`UPDATE worker_invocations SET state = ?, worker_id = '', started = 0, lease_expires_unix_ms = 0,
							in_doubt = 0, in_doubt_until_unix_ms = 0 WHERE client_ref = ?`,
						stateWaiting, inv.ClientRef); err != nil {
						return err
					}
					if err := appendReset(ctx, tx, inv.ClientRef); err != nil {
						return err
					}
					touched[inv.Pool] = true
					continue
				}
				if err := markInDoubtTx(ctx, tx, inv, s.now()); err != nil {
					return err
				}
				touched[inv.Pool] = true
			}
		}
		for pool := range touched {
			rows, err := tx.Query(ctx, `SELECT DISTINCT worker_id FROM worker_invocations WHERE pool_id = ? AND worker_id <> ''`, pool)
			if err != nil {
				return err
			}
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			_ = rows.Close()
			for _, id := range ids {
				if err := setUnitsInUse(ctx, tx, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func failRow(ctx context.Context, tx *psql.Execer, clientRef string, code core.Code, charged int64) error {
	_, err := tx.Exec(ctx,
		`UPDATE worker_invocations SET state = ?, code = ?, charged_mpts = ?, in_doubt = 0,
			in_doubt_until_unix_ms = 0, cancel_requested = 0 WHERE client_ref = ?`,
		stateFailed, string(code), charged, clientRef)
	return err
}

func markInDoubtTx(ctx context.Context, tx *psql.Execer, inv Invocation, now time.Time) error {
	pool, err := poolGrace(ctx, tx, inv.Pool)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE worker_invocations SET state = ?, in_doubt = 1, in_doubt_until_unix_ms = ?, lease_expires_unix_ms = 0
		 WHERE client_ref = ?`, stateLeased, unixms(now.Add(pool)), inv.ClientRef)
	return err
}

func (s *Store) markInDoubt(ctx context.Context, inv Invocation) error {
	return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		return markInDoubtTx(ctx, tx, inv, s.now())
	})
}

func poolGrace(ctx context.Context, tx *psql.Execer, poolID string) (time.Duration, error) {
	var grace int64
	err := tx.QueryRow(ctx, `SELECT grace_ms FROM worker_pools WHERE pool_id = ?`, poolID).Scan(&grace)
	return time.Duration(grace) * time.Millisecond, err
}

func descriptorForTx(ctx context.Context, tx *psql.Execer, poolID, modelID string) (Descriptor, error) {
	var raw string
	err := tx.QueryRow(ctx,
		`SELECT descriptor FROM worker_pool_models WHERE pool_id = ? AND model = ?`, poolID, modelID).Scan(&raw)
	if core.CodeOf(err) == core.CodeNotFound {
		return Descriptor{}, core.NewError(core.CodeNotFound,
			fmt.Sprintf("pool %q does not serve %q", poolID, modelID), map[string]any{"pool": poolID, "model": modelID})
	}
	if err != nil {
		return Descriptor{}, err
	}
	var d Descriptor
	err = json.Unmarshal([]byte(raw), &d)
	return d, err
}

func appendReset(ctx context.Context, tx *psql.Execer, clientRef string) error {
	var seq sql.NullInt64
	if err := tx.QueryRow(ctx,
		`SELECT MAX(seq) FROM worker_events WHERE client_ref = ?`, clientRef).Scan(&seq); err != nil {
		return err
	}
	next := seq.Int64 + 1
	_, err := tx.Exec(ctx,
		`INSERT INTO worker_events (client_ref, seq, kind, data) VALUES (?, ?, ?, '{}')`,
		clientRef, next, "reset")
	return err
}

// EventsAfter lists the partial output a follower has not seen.
func (s *Store) EventsAfter(ctx context.Context, clientRef string, after int64) ([]Event, error) {
	rows, err := s.db.Execer().Query(ctx,
		`SELECT seq, kind, data FROM worker_events WHERE client_ref = ? AND seq > ? ORDER BY seq`, clientRef, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		var data string
		if err := rows.Scan(&e.Seq, &e.Kind, &data); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Waiting counts the invocations waiting in a pool.
func (s *Store) Waiting(ctx context.Context, poolID string) (int, error) {
	var n int
	err := s.db.Execer().QueryRow(ctx,
		`SELECT COUNT(*) FROM worker_invocations WHERE pool_id = ? AND state = ?`, poolID, stateWaiting).Scan(&n)
	return n, err
}

// Metrics reads the queue metrics ADR-0012 §11 asks an operator to see.
func (s *Store) Metrics(ctx context.Context, poolID string) (QueueMetrics, error) {
	m := QueueMetrics{
		Pool:       poolID,
		ByClass:    map[Class]int{},
		ByModel:    map[string]int{},
		Workers:    map[State]int{},
		QueueDepth: map[string]int{},
	}
	rows, err := s.db.Execer().Query(ctx, selectInvocation+` WHERE pool_id = ?`, poolID)
	if err != nil {
		return m, err
	}
	now := s.now()
	for rows.Next() {
		inv, err := scanInvocation(rows)
		if err != nil {
			_ = rows.Close()
			return m, err
		}
		switch inv.State {
		case stateWaiting:
			m.Waiting++
			m.ByClass[inv.Class]++
			m.ByModel[inv.Model]++
			m.QueueDepth[inv.Model]++
			if wait := now.Sub(inv.QueuedAt); wait > m.OldestWait {
				m.OldestWait = wait
			}
		case stateLeased:
			m.Leased++
			m.UnitsInUse += inv.Units
			m.ByClass[inv.Class]++
			if age := now.Sub(inv.QueuedAt); age > m.MaxLeaseAge {
				m.MaxLeaseAge = age
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return m, err
	}
	_ = rows.Close()
	workers, err := s.ListWorkers(ctx, poolID)
	if err != nil {
		return m, err
	}
	for _, w := range workers {
		m.Workers[w.State]++
	}
	return m, nil
}

func isTerminal(state string) bool {
	switch state {
	case stateSucceeded, stateFailed, stateCancelled, stateRemote:
		return true
	}
	return false
}
