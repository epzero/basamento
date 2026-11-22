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
	stateSpilling  = "spilling"
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
		  queued_unix_ms, leased_unix_ms, not_before_unix_ms, lease_expires_unix_ms, in_doubt_until_unix_ms, queue_timeout_ms, max_runtime_ms,
		  cancel_requested, reserved_mpts, charged_mpts, code, request, result, provider_handle, spilled, trace_parent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', 0, 0, 0, ?, 0, 0, 0, 0, ?, ?, 0, ?, 0, '', ?, '', '', 0, ?)
		 ON CONFLICT (client_ref) DO NOTHING`,
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
	started, in_doubt, queued_unix_ms, leased_unix_ms, not_before_unix_ms, lease_expires_unix_ms, in_doubt_until_unix_ms,
	queue_timeout_ms, max_runtime_ms, cancel_requested, reserved_mpts, charged_mpts, code, request, result,
	provider_handle, spilled, trace_parent FROM worker_invocations`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInvocation(row rowScanner) (Invocation, error) {
	var (
		inv                                                      Invocation
		tenant, class, state, code                               string
		started, inDoubt, cancel                                 int
		queued, leased, notBefore, lease, doubt, timeout, maxRun int64
		request, result                                          string
		spilled                                                  int
	)
	err := row.Scan(&inv.ClientRef, &inv.Pool, &tenant, &inv.Model, &class, &inv.Units, &state, &inv.WorkerID,
		&inv.Attempt, &started, &inDoubt, &queued, &leased, &notBefore, &lease, &doubt, &timeout, &maxRun, &cancel,
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
	if leased != 0 {
		inv.LeasedAt = fromms(leased)
	}
	if notBefore != 0 {
		inv.NotBefore = fromms(notBefore)
	}
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
// own order; the worker only says how much it has free (ADR-0012 §5). A
// positive Wait makes it a long poll: the call returns as soon as work arrives
// or, with nothing to hand out, when the wait has elapsed.
func (s *Store) Lease(ctx context.Context, req LeaseRequest) ([]Invocation, error) {
	var deadline time.Time
	if req.Wait > 0 {
		deadline = s.now().Add(req.Wait)
	}
	w, err := s.validWorker(ctx, req.WorkerID, req.Credential)
	if err != nil {
		return nil, err
	}
	if err := s.touchWorker(ctx, w.ID); err != nil {
		return nil, err
	}
	for {
		if w.State != StateActive || req.FreeUnits <= 0 {
			return nil, nil
		}
		leased, err := s.leaseOnce(ctx, w, req)
		if err != nil {
			return nil, err
		}
		if len(leased) > 0 || req.Wait <= 0 {
			return leased, nil
		}
		remaining := deadline.Sub(s.now())
		if remaining <= 0 {
			return nil, nil
		}
		// The bus wake is a later work package; until then the long poll
		// re-reads the queue at a bounded interval.
		step := 100 * time.Millisecond
		if remaining < step {
			step = remaining
		}
		select {
		case <-s.after(step):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// The worker may have been paused or removed while it waited.
		w, err = s.validWorker(ctx, req.WorkerID, req.Credential)
		if err != nil {
			return nil, err
		}
	}
}

// after waits on the store's clock. The fake clock in tests implements After,
// so a long poll is deterministic there; the system clock uses time.After.
func (s *Store) after(d time.Duration) <-chan time.Time {
	if c, ok := s.clock.(interface {
		After(time.Duration) <-chan time.Time
	}); ok {
		return c.After(d)
	}
	return time.After(d)
}

func (s *Store) leaseOnce(ctx context.Context, w Worker, req LeaseRequest) ([]Invocation, error) {
	pool, err := s.Pool(ctx, w.Pool)
	if err != nil {
		return nil, err
	}
	warm := make(map[string]bool, len(req.WarmModels))
	for _, modelID := range req.WarmModels {
		warm[modelID] = true
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
		now := s.now()
		candidates, err := s.waitingCandidates(ctx, tx, w.Pool, models, now)
		if err != nil {
			return err
		}
		leasedCounts, err := leasedByTenant(ctx, tx, w.Pool)
		if err != nil {
			return err
		}
		chosen, err := choose(candidates, pool, now, free, req.TimeLeft, warm,
			leasedCounts, func(t core.TenantID) (TenantSettings, error) {
				return tenantSettingsTx(ctx, tx, w.Pool, t)
			})
		if err != nil {
			return err
		}
		for _, inv := range chosen {
			expires := now.Add(pool.Lease)
			taken, err := leaseRow(ctx, tx, inv, w.ID, now, expires)
			if err != nil {
				return err
			}
			if !taken {
				// Another replica took the row between the read and the
				// update; leave it to that replica.
				continue
			}
			inv.State = stateLeased
			inv.WorkerID = w.ID
			inv.Attempt++
			inv.Started = false
			inv.InDoubt = false
			inv.LeasedAt = now
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

// leaseRow moves one waiting invocation to workerID. It reports whether this
// call won the row: another replica may have leased it, or given it a
// not-before time, between the candidate read and this update, and a lease
// must never return a row it did not take (REQ-MODEL-012, REQ-MODEL-018).
func leaseRow(ctx context.Context, tx *psql.Execer, inv Invocation, workerID string, now, expires time.Time) (bool, error) {
	res, err := tx.Exec(ctx,
		`UPDATE worker_invocations SET state = ?, worker_id = ?, attempt = attempt + 1,
			started = 0, in_doubt = 0, provider_handle = '', leased_unix_ms = ?, lease_expires_unix_ms = ?,
			in_doubt_until_unix_ms = 0, not_before_unix_ms = 0
		 WHERE client_ref = ? AND state = ? AND worker_id = ? AND not_before_unix_ms <= ?
		   AND cancel_requested = 0 AND attempt < 3`,
		stateLeased, workerID, unixms(now), unixms(expires), inv.ClientRef, stateWaiting, inv.WorkerID, unixms(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	// A row that still names an earlier worker is a takeover: tell that
	// worker to cancel at its provider in its next heartbeat (ADR-0012 §7).
	if inv.WorkerID != "" && inv.WorkerID != workerID {
		if err := addCancelTx(ctx, tx, inv.ClientRef, inv.WorkerID, inv.Attempt); err != nil {
			return false, err
		}
	}
	return true, nil
}

// addCancelTx records that workerID must cancel clientRef at its provider.
func addCancelTx(ctx context.Context, tx *psql.Execer, clientRef, workerID string, attempt int) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO worker_cancels (client_ref, worker_id, attempt) VALUES (?, ?, ?)
		 ON CONFLICT (client_ref, worker_id) DO UPDATE SET attempt = excluded.attempt`,
		clientRef, workerID, attempt)
	return err
}

// waitingCandidates lists the pool's waiting invocations this worker can serve,
// oldest first, before ordering.
func (s *Store) waitingCandidates(ctx context.Context, exec *psql.Execer, poolID string, models map[string]Descriptor, now time.Time) ([]Invocation, error) {
	rows, err := exec.Query(ctx, selectInvocation+` WHERE pool_id = ? AND state = ? AND not_before_unix_ms <= ?
		AND cancel_requested = 0 ORDER BY queued_unix_ms, client_ref`, poolID, stateWaiting, unixms(now))
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
// fairness in the shared pool, then age, then a model the worker already has
// loaded (REQ-MODEL-015 to REQ-MODEL-019, ADR-0012 §5). The per-tenant
// in-flight counts start from the leased rows and move with every pick, so one
// call never exceeds a tenant's concurrency limit and the shared pool
// alternates between tenants.
func choose(candidates []Invocation, pool PoolSpec, now time.Time,
	free int, timeLeft time.Duration, warm map[string]bool, leased map[core.TenantID]int,
	settings func(core.TenantID) (TenantSettings, error)) ([]Invocation, error) {

	type ranked struct {
		inv         Invocation
		rank        int
		weight      int
		concurrency int
		warm        bool
		when        time.Time
	}
	rankedList := make([]ranked, 0, len(candidates))
	for _, inv := range candidates {
		settingsFor, err := settings(inv.Tenant)
		if err != nil {
			return nil, err
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
			inv:         inv,
			rank:        rank,
			weight:      weight,
			concurrency: settingsFor.Concurrency,
			warm:        warm[inv.Model],
			when:        inv.QueuedAt,
		})
	}
	counts := map[core.TenantID]int{}
	for tenant, n := range leased {
		counts[tenant] = n
	}
	var out []Invocation
	remaining := free
	for remaining > 0 && len(rankedList) > 0 {
		sort.SliceStable(rankedList, func(i, j int) bool {
			a, b := rankedList[i], rankedList[j]
			if a.rank != b.rank {
				return a.rank < b.rank
			}
			if pool.Tenant == "" {
				ar := float64(counts[a.inv.Tenant]) / float64(a.weight)
				br := float64(counts[b.inv.Tenant]) / float64(b.weight)
				if ar != br {
					return ar < br
				}
			}
			if !a.when.Equal(b.when) {
				return a.when.Before(b.when)
			}
			if a.warm != b.warm {
				return a.warm
			}
			return a.inv.ClientRef < b.inv.ClientRef
		})
		picked := -1
		for i, r := range rankedList {
			if pool.Tenant == "" && r.concurrency > 0 && counts[r.inv.Tenant] >= r.concurrency {
				continue
			}
			if r.inv.Units > remaining {
				continue
			}
			if r.inv.MaxRuntime > 0 && timeLeft > 0 && r.inv.MaxRuntime > timeLeft {
				continue
			}
			picked = i
			break
		}
		if picked < 0 {
			break
		}
		r := rankedList[picked]
		out = append(out, r.inv)
		counts[r.inv.Tenant]++
		remaining -= r.inv.Units
		rankedList = append(rankedList[:picked], rankedList[picked+1:]...)
	}
	return out, nil
}

// leaseForReport authenticates a worker and returns the invocation its report
// names. Every lease-scoped report carries its attempt: a report from an
// earlier attempt, another worker's row or a terminal invocation is lease_lost
// (REQ-MODEL-024). The attempt also guards the write, so a report that races a
// sweep cannot overwrite a newer state.
func (s *Store) leaseForReport(ctx context.Context, workerID, credential, clientRef string, attempt int) (Worker, Invocation, error) {
	w, err := s.validWorker(ctx, workerID, credential)
	if err != nil {
		return Worker{}, Invocation{}, err
	}
	inv, err := s.Invocation(ctx, clientRef)
	if err != nil {
		return Worker{}, Invocation{}, err
	}
	if isTerminal(inv.State) || inv.State != stateLeased || inv.WorkerID != w.ID || inv.Attempt != attempt {
		return Worker{}, Invocation{}, errLeaseLost(clientRef)
	}
	if err := s.refusePastGrace(inv); err != nil {
		return Worker{}, Invocation{}, err
	}
	return w, inv, nil
}

// affected reports whether a guarded write won the row; a zero row count means
// another report, a sweep or a newer attempt changed it first.
func affected(res interface{ RowsAffected() (int64, error) }, clientRef string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errLeaseLost(clientRef)
	}
	return nil
}

// Starting records that a worker is about to call its provider. An invocation
// whose lease expired is recovered to its own worker only inside the grace
// period (REQ-MODEL-021, REQ-MODEL-023).
func (s *Store) Starting(ctx context.Context, workerID, credential, clientRef string, attempt int) error {
	w, inv, err := s.leaseForReport(ctx, workerID, credential, clientRef, attempt)
	if err != nil {
		return err
	}
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return err
	}
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET started = 1, in_doubt = 0, in_doubt_until_unix_ms = 0,
			lease_expires_unix_ms = ? WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
		unixms(s.now().Add(pool.Lease)), clientRef, stateLeased, w.ID, attempt)
	if err != nil {
		return err
	}
	return affected(res, clientRef)
}

// Started records the provider's handle after a start.
func (s *Store) Started(ctx context.Context, workerID, credential, clientRef, providerHandle string, attempt int) error {
	w, inv, err := s.leaseForReport(ctx, workerID, credential, clientRef, attempt)
	if err != nil {
		return err
	}
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return err
	}
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET started = 1, in_doubt = 0, in_doubt_until_unix_ms = 0,
			lease_expires_unix_ms = ?, provider_handle = ?
		 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
		unixms(s.now().Add(pool.Lease)), providerHandle, clientRef, stateLeased, w.ID, attempt)
	if err != nil {
		return err
	}
	return affected(res, clientRef)
}

// Resume lets a worker take back an in-doubt invocation inside the grace
// period (REQ-MODEL-023).
func (s *Store) Resume(ctx context.Context, workerID, credential, clientRef string, attempt int) error {
	w, inv, err := s.leaseForReport(ctx, workerID, credential, clientRef, attempt)
	if err != nil {
		return err
	}
	if !inv.InDoubt {
		return errLeaseLost(clientRef)
	}
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return err
	}
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET in_doubt = 0, in_doubt_until_unix_ms = 0, lease_expires_unix_ms = ?
		 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
		unixms(s.now().Add(pool.Lease)), clientRef, stateLeased, w.ID, attempt)
	if err != nil {
		return err
	}
	return affected(res, clientRef)
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
func (s *Store) Events(ctx context.Context, workerID, credential, clientRef string, attempt int, events []Event) error {
	w, inv, err := s.leaseForReport(ctx, workerID, credential, clientRef, attempt)
	if err != nil {
		return err
	}
	if len(events) > MaxEventsPerBatch {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("worker: more than %d events in one report", MaxEventsPerBatch), nil)
	}
	var bytes int64
	for _, e := range events {
		if n := int64(len(e.Text) + len(e.Audio) + len(e.Data)); n > MaxEventBytes {
			return core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("worker: event larger than %d bytes", MaxEventBytes), nil)
		}
		bytes += int64(len(e.Text) + len(e.Audio) + len(e.Data))
	}
	if bytes > MaxEventBatchBytes {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("worker: event batch larger than %d bytes", MaxEventBatchBytes), nil)
	}
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		// The no-op update takes the row lock and checks the attempt in one
		// statement, so a sweep cannot end the invocation between the read
		// and the inserts.
		guard, err := tx.Exec(ctx,
			`UPDATE worker_invocations SET in_doubt = in_doubt
			 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
			clientRef, stateLeased, w.ID, attempt)
		if err != nil {
			return err
		}
		if err := affected(guard, clientRef); err != nil {
			return err
		}
		if inv.InDoubt {
			if _, err := tx.Exec(ctx,
				`UPDATE worker_invocations SET in_doubt = 0, in_doubt_until_unix_ms = 0,
					lease_expires_unix_ms = ? WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
				unixms(s.now().Add(pool.Lease)), clientRef, stateLeased, w.ID, attempt); err != nil {
				return err
			}
		}
		var last int64
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(seq), 0) FROM worker_events WHERE client_ref = ?`, clientRef).Scan(&last); err != nil {
			return err
		}
		if last+int64(len(events)) > MaxEventsPerInvocation {
			return core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("worker: more than %d events for one invocation", MaxEventsPerInvocation), nil)
		}
		for _, e := range events {
			// The sequence is the store's: a worker that restarted, a new
			// attempt or a reset can never collide with or go behind what a
			// follower has already seen (REQ-MODEL-023).
			if e.Seq <= last {
				e.Seq = last + 1
			}
			last = e.Seq
			data, err := json.Marshal(e)
			if err != nil {
				return core.NewError(core.CodeInvalidArgument, "worker: event is not encodable", nil)
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
// (REQ-MODEL-024). The worker's cost is bounded: ADR-0012 §9 prices an
// invocation on the platform, so a worker cannot charge more than the
// reservation or the configured ceiling.
func (s *Store) Finish(ctx context.Context, workerID, credential, clientRef, state string, result json.RawMessage, code core.Code, cost int64, attempt int) error {
	w, inv, err := s.leaseForReport(ctx, workerID, credential, clientRef, attempt)
	if err != nil {
		return err
	}
	switch state {
	case stateSucceeded, stateFailed, stateCancelled:
	default:
		return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("unknown result state %q", state), nil)
	}
	if cost < 0 {
		return core.NewError(core.CodeInvalidArgument, "worker: cost must not be negative", nil)
	}
	cost = s.boundCost(inv, cost)
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	err = s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		res, err := tx.Exec(ctx,
			`UPDATE worker_invocations SET state = ?, charged_mpts = ?, code = ?, result = ?,
				in_doubt = 0, in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0,
				leased_unix_ms = 0, cancel_requested = 0, ended_unix_ms = ?
			 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
			state, cost, string(code), string(result), unixms(s.now()),
			clientRef, stateLeased, w.ID, attempt)
		if err != nil {
			return err
		}
		if err := affected(res, clientRef); err != nil {
			return err
		}
		if err := clearCancels(ctx, tx, clientRef); err != nil {
			return err
		}
		return setUnitsInUse(ctx, tx, w.ID)
	})
	return err
}

// boundCost bounds the cost a worker reports (ADR-0012 §9): the platform
// owns the price, so the reported amount may never exceed the reservation the
// platform made or its configured ceiling.
func (s *Store) boundCost(inv Invocation, cost int64) int64 {
	ceiling := s.maxCost
	if inv.ReservedMpts > 0 && inv.ReservedMpts < ceiling {
		ceiling = inv.ReservedMpts
	}
	if cost > ceiling {
		return ceiling
	}
	return cost
}

// Release gives a lease back. Unused says the worker never started the
// invocation, so it goes back to the queue even for a model that is not
// restart-safe; otherwise a started, non-restart-safe invocation stays in
// doubt for its own worker. NotBefore is the vendor's rate limit.
func (s *Store) Release(ctx context.Context, workerID, credential, clientRef string, unused bool, notBefore time.Time, attempt int) error {
	w, inv, err := s.leaseForReport(ctx, workerID, credential, clientRef, attempt)
	if err != nil {
		return err
	}
	if inv.CancelRequested {
		// Work the run no longer wants must not be requeued and started.
		return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
			if err := cancelLeasedTx(ctx, tx, inv, s.now()); err != nil {
				return err
			}
			return setUnitsInUse(ctx, tx, w.ID)
		})
	}
	restartSafe := false
	if inv.Started && !unused {
		// A started, non-restart-safe invocation stays in doubt for its own
		// worker; a restart-safe one goes back to the queue.
		d, err := s.descriptorFor(ctx, inv.Pool, inv.Model)
		if err != nil {
			return err
		}
		if !d.RestartSafe {
			return s.markInDoubt(ctx, inv)
		}
		restartSafe = true
	}
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return err
	}
	// A release must not restart the queue timeout or the ageing clock: the
	// row keeps its original queued time. "Not before" is the vendor's rate
	// limit and only delays the next lease, so it gets its own column; a
	// worker cannot park work past one lease period.
	now := s.now()
	var notBeforeMS int64
	if !notBefore.IsZero() {
		if limit := now.Add(pool.Lease); notBefore.After(limit) {
			notBefore = limit
		}
		if notBefore.After(now) {
			notBeforeMS = unixms(notBefore)
		}
	}
	return s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		res, err := tx.Exec(ctx,
			`UPDATE worker_invocations SET state = ?, worker_id = '', started = 0, in_doubt = 0,
				in_doubt_until_unix_ms = 0, provider_handle = '', lease_expires_unix_ms = 0,
				leased_unix_ms = 0, not_before_unix_ms = ?
			 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
			stateWaiting, notBeforeMS, clientRef, stateLeased, w.ID, attempt)
		if err != nil {
			return err
		}
		if err := affected(res, clientRef); err != nil {
			return err
		}
		if restartSafe {
			// Followers see a fresh output stream on the next attempt
			// (REQ-MODEL-021).
			if err := appendReset(ctx, tx, clientRef); err != nil {
				return err
			}
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
	pool, err := s.Pool(ctx, w.Pool)
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	expires := s.now().Add(pool.Lease)
	now := s.now()
	rotated, err := newToken()
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	err = s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		// Rotate the credential, keeping the one the worker presented valid
		// until its own expiry: a lost heartbeat response must not lock a
		// live worker out (ADR-0012 §10).
		// The credential the worker presented becomes the predecessor, whatever
		// it was: a worker that did not receive the last rotation keeps its
		// credential and still receives a new one (ADR-0012 §10).
		presented := hashToken(credential)
		if _, err := tx.Exec(ctx,
			`UPDATE worker_workers
			 SET heartbeat_ms = ?, present = CASE WHEN removed = 0 THEN 1 ELSE present END,
			     previous_credential_hash = ?,
			     previous_credential_expires_unix_ms = CASE WHEN credential_hash = ? THEN credential_expires_unix_ms
			                                              ELSE previous_credential_expires_unix_ms END,
			     credential_hash = ?, credential_expires_unix_ms = ?
			 WHERE worker_id = ?`,
			unixms(now), presented, presented, hashToken(rotated), unixms(now.Add(s.credentialTTL())), w.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE worker_invocations SET lease_expires_unix_ms = ?
			 WHERE worker_id = ? AND state = ? AND in_doubt = 0`,
			unixms(expires), w.ID, stateLeased); err != nil {
			return err
		}
		if freeUnits >= 0 {
			// The request carries the units the worker has free; the column
			// is the units in use (ADR-0012 §6, §11).
			used := w.Capacity - freeUnits
			if used < 0 {
				used = 0
			}
			if used > w.Capacity {
				used = w.Capacity
			}
			if _, err := tx.Exec(ctx, `UPDATE worker_workers SET units_used = ? WHERE worker_id = ?`, used, w.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	answer := HeartbeatAnswer{
		Drain: w.State == StateDraining, LeaseExpires: expires, State: w.State,
		Credential: rotated, CancelAttempts: map[string]int{},
	}
	seen := map[string]bool{}
	add := func(ref string) {
		if !seen[ref] {
			seen[ref] = true
			answer.Cancel = append(answer.Cancel, ref)
		}
	}
	rows, err := s.db.Execer().Query(ctx,
		`SELECT client_ref, attempt FROM worker_invocations WHERE worker_id = ? AND state = ? AND cancel_requested = 1 ORDER BY client_ref`,
		w.ID, stateLeased)
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	for rows.Next() {
		var ref string
		var attempt int
		if err := rows.Scan(&ref, &attempt); err != nil {
			_ = rows.Close()
			return HeartbeatAnswer{}, err
		}
		add(ref)
		answer.CancelAttempts[ref] = attempt
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return HeartbeatAnswer{}, err
	}
	_ = rows.Close()
	// A superseded attempt is told to cancel at its provider even though the
	// invocation now belongs to another worker (ADR-0012 §7).
	cancels, err := s.db.Execer().Query(ctx,
		`SELECT client_ref, attempt FROM worker_cancels WHERE worker_id = ? ORDER BY client_ref`, w.ID)
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	for cancels.Next() {
		var ref string
		var attempt int
		if err := cancels.Scan(&ref, &attempt); err != nil {
			_ = cancels.Close()
			return HeartbeatAnswer{}, err
		}
		add(ref)
		if attempt > 0 {
			answer.CancelAttempts[ref] = attempt
		}
	}
	if err := cancels.Err(); err != nil {
		_ = cancels.Close()
		return HeartbeatAnswer{}, err
	}
	_ = cancels.Close()
	// The rows stay until the invocation is terminal: a dropped heartbeat
	// response must not lose a cancel (REQ-MODEL-026).
	return answer, nil
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
			`UPDATE worker_invocations SET state = ?, charged_mpts = 0, code = ?, ended_unix_ms = ?
			 WHERE client_ref = ? AND state = ?`,
			stateCancelled, string(core.CodeCancelled), unixms(s.now()), clientRef, stateWaiting)
		return err
	}
	_, err = s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET cancel_requested = 1 WHERE client_ref = ?`, clientRef)
	return err
}

// Recover extends every lease by one period when the platform starts, so an
// outage of its own does not turn into lost work (REQ-MODEL-027). It only ever
// moves a deadline forward: a lease or in-doubt period that already reaches
// further than the recovered deadline keeps its value.
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
			// Extend from the restart: an outage of the platform must not
			// turn into lost work, whatever it lasted (REQ-MODEL-027). CASE
			// keeps a deadline that is already later.
			lease := restart + pool.Lease.Milliseconds()
			if _, err := tx.Exec(ctx,
				`UPDATE worker_invocations
				 SET lease_expires_unix_ms = CASE WHEN lease_expires_unix_ms < ? THEN ? ELSE lease_expires_unix_ms END
				 WHERE pool_id = ? AND state IN (?, ?) AND lease_expires_unix_ms > 0`,
				lease, lease, pool.ID, stateLeased, stateSpilling); err != nil {
				return err
			}
			grace := restart + pool.Grace.Milliseconds()
			if _, err := tx.Exec(ctx,
				`UPDATE worker_invocations
				 SET in_doubt_until_unix_ms = CASE WHEN in_doubt_until_unix_ms < ? THEN ? ELSE in_doubt_until_unix_ms END
				 WHERE pool_id = ? AND state = ? AND in_doubt_until_unix_ms > 0`,
				grace, grace, pool.ID, stateLeased); err != nil {
				return err
			}
		}
		return nil
	})
}

// Sweep applies the queue timeout, expired leases, restart-safe redelivery and
// the third attempt's worker_lost (REQ-MODEL-014, -021, -022, -025).
func (s *Store) Sweep(ctx context.Context) error { return s.sweep(ctx) }

// RunJanitor applies the queue's time-driven rules every interval until ctx
// ends. The platform process owns it: a pool with no worker still times out,
// expires its leases and forgets ended invocations (REQ-MODEL-014, -025, -088).
func (s *Store) RunJanitor(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.sweep(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

func (s *Store) sweep(ctx context.Context) error {
	var gone []absentWorker
	err := s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		now := s.now()
		var err error
		gone, err = s.expireWorkersTx(ctx, tx, now)
		if err != nil {
			return err
		}
		if err := s.evictEndedTx(ctx, tx, now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, selectInvocation+` WHERE state IN (?, ?, ?)`, stateWaiting, stateLeased, stateSpilling)
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
				if inv.Attempt >= 3 {
					if err := failRow(ctx, tx, inv, CodeWorkerLost, inv.ReservedMpts, now); err != nil {
						return err
					}
					touched[inv.Pool] = true
					continue
				}
				if inv.CancelRequested {
					if err := cancelRowTx(ctx, tx, inv, 0, now); err != nil {
						return err
					}
					touched[inv.Pool] = true
					continue
				}
				if inv.QueueTimeout > 0 && !now.Before(inv.QueuedAt.Add(inv.QueueTimeout)) {
					if err := failRow(ctx, tx, inv, core.CodeUnavailable, 0, now); err != nil {
						return err
					}
					touched[inv.Pool] = true
				}
			case inv.State == stateSpilling:
				// A spilled invocation whose remote start has not reported
				// back by the claim deadline is failed rather than started a
				// second time. A cancel on a claimed row ends it cancelled and
				// keeps the reservation, because the provider may have run.
				switch {
				case inv.CancelRequested:
					if err := cancelRowTx(ctx, tx, inv, inv.ReservedMpts, now); err != nil {
						return err
					}
					touched[inv.Pool] = true
				case !inv.LeaseExpires.IsZero() && !now.Before(inv.LeaseExpires):
					if err := failRow(ctx, tx, inv, core.CodeUnavailable, inv.ReservedMpts, now); err != nil {
						return err
					}
					touched[inv.Pool] = true
				}
			case inv.InDoubt:
				if !inv.InDoubtUntil.IsZero() && !now.Before(inv.InDoubtUntil) {
					if err := failRow(ctx, tx, inv, CodeWorkerLost, inv.ReservedMpts, now); err != nil {
						return err
					}
					touched[inv.Pool] = true
				}
			case !inv.LeaseExpires.IsZero() && !now.Before(inv.LeaseExpires):
				if inv.Attempt >= 3 {
					if inv.Started && inv.WorkerID != "" {
						if err := addCancelTx(ctx, tx, inv.ClientRef, inv.WorkerID, inv.Attempt); err != nil {
							return err
						}
					}
					if err := failRow(ctx, tx, inv, CodeWorkerLost, inv.ReservedMpts, now); err != nil {
						return err
					}
					touched[inv.Pool] = true
					continue
				}
				if !inv.Started {
					if _, err := tx.Exec(ctx,
						`UPDATE worker_invocations SET state = ?, worker_id = '', provider_handle = '',
							lease_expires_unix_ms = 0, leased_unix_ms = 0, in_doubt = 0,
							in_doubt_until_unix_ms = 0, not_before_unix_ms = 0
						 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
						stateWaiting, inv.ClientRef, stateLeased, inv.WorkerID, inv.Attempt); err != nil {
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
					if inv.CancelRequested {
						if err := cancelRowTx(ctx, tx, inv, 0, now); err != nil {
							return err
						}
						touched[inv.Pool] = true
						continue
					}
					// The old attempt may still be alive; it is told to
					// cancel while the new attempt takes the work.
					if inv.WorkerID != "" {
						if err := addCancelTx(ctx, tx, inv.ClientRef, inv.WorkerID, inv.Attempt); err != nil {
							return err
						}
					}
					if _, err := tx.Exec(ctx,
						`UPDATE worker_invocations SET state = ?, worker_id = '', started = 0, provider_handle = '',
							lease_expires_unix_ms = 0, leased_unix_ms = 0, in_doubt = 0,
							in_doubt_until_unix_ms = 0, not_before_unix_ms = 0
						 WHERE client_ref = ? AND state = ? AND attempt = ?`,
						stateWaiting, inv.ClientRef, stateLeased, inv.Attempt); err != nil {
						return err
					}
					if err := appendReset(ctx, tx, inv.ClientRef); err != nil {
						return err
					}
					touched[inv.Pool] = true
					continue
				}
				if err := markInDoubtTx(ctx, tx, inv, s.now()); err != nil {
					// A report ended or requeued the row first; the sweep
					// leaves the newer outcome alone.
					if core.CodeOf(err) == CodeLeaseLost {
						continue
					}
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
	if err != nil {
		return err
	}
	for _, w := range gone {
		_ = s.auditEventFor(ctx, w.Tenant, "worker.absent", w.Pool, "allowed", "heartbeat")
	}
	return nil
}

// evictEndedTx forgets ended invocations and their events after the retention
// window and above the ended bound, oldest ended first (REQ-MODEL-088).
func (s *Store) evictEndedTx(ctx context.Context, tx *psql.Execer, now time.Time) error {
	rows, err := tx.Query(ctx,
		`SELECT client_ref, ended_unix_ms FROM worker_invocations WHERE ended_unix_ms > 0
		 ORDER BY ended_unix_ms DESC, client_ref`)
	if err != nil {
		return err
	}
	var keep int
	var drop []string
	cutoff := unixms(now.Add(-s.retention))
	for rows.Next() {
		var ref string
		var ended int64
		if err := rows.Scan(&ref, &ended); err != nil {
			_ = rows.Close()
			return err
		}
		keep++
		if ended <= cutoff || keep > s.maxEnded {
			drop = append(drop, ref)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, ref := range drop {
		if _, err := tx.Exec(ctx, `DELETE FROM worker_events WHERE client_ref = ?`, ref); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM worker_cancels WHERE client_ref = ?`, ref); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM worker_invocations WHERE client_ref = ? AND ended_unix_ms > 0`, ref); err != nil {
			return err
		}
	}
	return nil
}

func failRow(ctx context.Context, tx *psql.Execer, inv Invocation, code core.Code, charged int64, now time.Time) error {
	_, err := tx.Exec(ctx,
		`UPDATE worker_invocations SET state = ?, code = ?, charged_mpts = ?, in_doubt = 0,
			in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0, leased_unix_ms = 0,
			cancel_requested = 0, ended_unix_ms = ? WHERE client_ref = ? AND state = ? AND attempt = ?`,
		stateFailed, string(code), charged, unixms(now), inv.ClientRef, inv.State, inv.Attempt)
	if err != nil {
		return err
	}
	return clearCancels(ctx, tx, inv.ClientRef)
}

// cancelRowTx ends a row the caller asked to cancel, keeping the cost the
// provider may already have incurred.
func cancelRowTx(ctx context.Context, tx *psql.Execer, inv Invocation, charged int64, now time.Time) error {
	// The state and attempt guard the write: a concurrent report that already
	// ended or requeued the row wins, and the sweep leaves it alone.
	_, err := tx.Exec(ctx,
		`UPDATE worker_invocations SET state = ?, code = ?, charged_mpts = ?, in_doubt = 0,
			in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0, leased_unix_ms = 0,
			cancel_requested = 0, ended_unix_ms = ? WHERE client_ref = ? AND state = ? AND attempt = ?`,
		stateCancelled, string(core.CodeCancelled), charged, unixms(now), inv.ClientRef, inv.State, inv.Attempt)
	if err != nil {
		return err
	}
	return clearCancels(ctx, tx, inv.ClientRef)
}

// cancelLeasedTx ends one attempt as cancelled, guarded by its worker and
// attempt, and keeps no charge for work nobody wanted.
func cancelLeasedTx(ctx context.Context, tx *psql.Execer, inv Invocation, now time.Time) error {
	res, err := tx.Exec(ctx,
		`UPDATE worker_invocations SET state = ?, code = ?, charged_mpts = 0, in_doubt = 0,
			in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0, leased_unix_ms = 0,
			cancel_requested = 0, ended_unix_ms = ?
		 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
		stateCancelled, string(core.CodeCancelled), unixms(now), inv.ClientRef, stateLeased, inv.WorkerID, inv.Attempt)
	if err != nil {
		return err
	}
	if err := affected(res, inv.ClientRef); err != nil {
		return err
	}
	return clearCancels(ctx, tx, inv.ClientRef)
}

// clearCancels drops the cancel hints of an invocation that ended.
func clearCancels(ctx context.Context, tx *psql.Execer, clientRef string) error {
	_, err := tx.Exec(ctx, `DELETE FROM worker_cancels WHERE client_ref = ?`, clientRef)
	return err
}

func markInDoubtTx(ctx context.Context, tx *psql.Execer, inv Invocation, now time.Time) error {
	pool, err := poolGrace(ctx, tx, inv.Pool)
	if err != nil {
		return err
	}
	res, err := tx.Exec(ctx,
		`UPDATE worker_invocations SET state = ?, in_doubt = 1, in_doubt_until_unix_ms = ?, lease_expires_unix_ms = 0
		 WHERE client_ref = ? AND state = ? AND worker_id = ? AND attempt = ?`,
		stateLeased, unixms(now.Add(pool)), inv.ClientRef, stateLeased, inv.WorkerID, inv.Attempt)
	if err != nil {
		return err
	}
	return affected(res, inv.ClientRef)
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
		var seq int64
		var kind, data string
		if err := rows.Scan(&seq, &kind, &data); err != nil {
			return nil, err
		}
		var e Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return nil, core.NewError(core.CodeInternal, "worker: stored event is corrupt",
				map[string]any{"client_ref": clientRef, "seq": seq})
		}
		e.Seq = seq
		e.Kind = kind
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
			if !inv.LeasedAt.IsZero() {
				if age := now.Sub(inv.LeasedAt); age > m.MaxLeaseAge {
					m.MaxLeaseAge = age
				}
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

// refusePastGrace refuses a report from a worker whose in-doubt grace period
// has run out; the invocation is no longer that worker's to report
// (REQ-MODEL-023).
func (s *Store) refusePastGrace(inv Invocation) error {
	if inv.InDoubt && !inv.InDoubtUntil.IsZero() && !s.now().Before(inv.InDoubtUntil) {
		return errLeaseLost(inv.ClientRef)
	}
	return nil
}

func isTerminal(state string) bool {
	switch state {
	case stateSucceeded, stateFailed, stateCancelled, stateRemote:
		return true
	}
	return false
}
