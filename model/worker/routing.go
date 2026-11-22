package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

type labelsKey struct{}

// WithLabels returns a context whose routing requires the given worker labels
// (for example gpu=nvidia-a100 or region=eu-west).
func WithLabels(ctx context.Context, labels map[string]string) context.Context {
	if len(labels) == 0 {
		return ctx
	}
	return context.WithValue(ctx, labelsKey{}, normalizeLabels(labels))
}

// requiredLabels returns the labels a routing decision must match.
func requiredLabels(ctx context.Context) map[string]string {
	labels, _ := ctx.Value(labelsKey{}).(map[string]string)
	return labels
}

// labelsMatch reports whether a worker satisfies every required label.
func labelsMatch(worker, required map[string]string) bool {
	for k, v := range required {
		if worker[k] != v {
			return false
		}
	}
	return true
}

// descriptorSupports reports whether a descriptor serves op. An empty op means
// the caller did not name a capability and any model of that id matches.
func descriptorSupports(d model.Descriptor, op model.Capability) bool {
	if op == "" {
		return true
	}
	for _, c := range d.Caps {
		if c == op {
			return true
		}
	}
	return false
}

// Remote is another provider of a model: a cloud provider, a machine-local
// model or a remote invoker. Spill-over starts work on one of these when a
// tenant allows it (ADR-0012 §8).
type Remote struct {
	// Name identifies the provider in spill results and audit events.
	Name string
	// Invoker starts the invocation there.
	Invoker model.Invoker
	// Descriptors are the models the provider serves.
	Descriptors []model.Descriptor
	// Labels are matched against the labels a routing decision requires,
	// just as a worker's labels are.
	Labels map[string]string
	// Estimate is the reservation for one invocation, in milli-points.
	Estimate int64
}

// descriptor returns the descriptor a remote serves for modelID and op.
func (r Remote) descriptor(modelID string, op model.Capability) (model.Descriptor, bool) {
	for _, d := range r.Descriptors {
		if d.ID == modelID && descriptorSupports(d, op) {
			return d, true
		}
	}
	return model.Descriptor{}, false
}

func (r Remote) serves(modelID string, op model.Capability) bool {
	_, ok := r.descriptor(modelID, op)
	return ok
}

// Destination is where an invocation is started: a pool's queue or another
// provider.
type Destination struct {
	Pool     string
	Remote   *Remote
	Model    model.Descriptor
	Tenant   core.TenantID
	IsRemote bool
}

// Router chooses the tenant's own pool before another provider of the same
// model (REQ-MODEL-031) and honours the capability and labels the call
// requires.
type Router struct {
	store   *Store
	remotes []Remote
}

// NewRouter builds a router over a worker store and the other providers of
// the deployment.
func NewRouter(store *Store, remotes ...Remote) *Router {
	return &Router{store: store, remotes: remotes}
}

// Select routes one invocation without a capability constraint.
func (r *Router) Select(ctx context.Context, tenant core.TenantID, modelID string) (Destination, error) {
	return r.SelectFor(ctx, tenant, modelID, "")
}

// SelectFor routes one invocation. A tenant's own pool wins over a shared pool
// and either wins over another provider; a pool or remote must serve the
// requested capability and the worker labels the call requires. This work
// package has no run-start path: the BDD step exercises it, and kit run and the
// API that call it are a later work package.
func (r *Router) SelectFor(ctx context.Context, tenant core.TenantID, modelID string, op model.Capability) (Destination, error) {
	required := requiredLabels(ctx)
	pools, err := r.store.ListPools(ctx)
	if err != nil {
		return Destination{}, err
	}
	sort.Strings(pools)
	var shared string
	for _, id := range pools {
		pool, err := r.store.Pool(ctx, id)
		if err != nil {
			return Destination{}, err
		}
		d, err := r.store.descriptorFor(ctx, id, modelID)
		if err != nil {
			if core.CodeOf(err) == core.CodeNotFound {
				continue
			}
			return Destination{}, err
		}
		if !descriptorSupports(d.Descriptor, op) {
			continue
		}
		if len(required) > 0 {
			ok, err := r.store.poolHasLabels(ctx, id, modelID, op, required)
			if err != nil {
				return Destination{}, err
			}
			if !ok {
				continue
			}
		}
		if pool.Tenant != "" && pool.Tenant == tenant {
			return Destination{Pool: id, Model: d.Descriptor, Tenant: tenant}, nil
		}
		if pool.Tenant == "" && shared == "" {
			shared = id
		}
	}
	if shared != "" {
		d, err := r.store.descriptorFor(ctx, shared, modelID)
		return Destination{Pool: shared, Model: d.Descriptor, Tenant: tenant}, err
	}
	for i := range r.remotes {
		if !labelsMatch(r.remotes[i].Labels, required) {
			continue
		}
		if d, ok := r.remotes[i].descriptor(modelID, op); ok {
			remote := r.remotes[i]
			return Destination{Remote: &remote, IsRemote: true, Tenant: tenant, Model: d}, nil
		}
	}
	return Destination{}, core.NewError(core.CodeNotFound,
		fmt.Sprintf("no provider serves model %q", modelID), map[string]any{"model": modelID})
}

func (s *Store) poolHasLabels(ctx context.Context, poolID, modelID string, op model.Capability, labels map[string]string) (bool, error) {
	workers, err := s.ListWorkers(ctx, poolID)
	if err != nil {
		return false, err
	}
	for _, w := range workers {
		if !w.Present || w.Removed || !labelsMatch(w.Labels, labels) {
			continue
		}
		for _, d := range w.Descriptors {
			if d.ID == modelID && descriptorSupports(d.Descriptor, op) {
				return true, nil
			}
		}
	}
	return false, nil
}

// SpillResult records one invocation withdrawn from a queue for another
// provider.
type SpillResult struct {
	ClientRef string
	Pool      string
	Remote    string
	Reserved  int64
}

// Spill moves queued work of tenants that turned spill-over on to another
// provider of the model once it has waited their spill-over time. The row is
// claimed for the remote provider before its paid start, so no worker can lease
// it in between and a crash after the start leaves a claimed row that the
// sweep fails at its claim deadline rather than work that starts twice
// (REQ-MODEL-032, REQ-MODEL-033). Off stays off: a tenant with spill-over off
// never has work moved. A shared pool applies each invocation's tenant
// settings; a remote must carry the labels the call requires.
func (s *Store) Spill(ctx context.Context, remotes ...Remote) ([]SpillResult, error) {
	pools, err := s.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	required := requiredLabels(ctx)
	now := s.now()
	var out []SpillResult
	for _, poolID := range pools {
		pool, err := s.Pool(ctx, poolID)
		if err != nil {
			return nil, err
		}
		waiting, err := s.waiting(ctx, poolID)
		if err != nil {
			return nil, err
		}
		settings := map[core.TenantID]TenantSettings{}
		for _, inv := range waiting {
			tenant := inv.Tenant
			if pool.Tenant != "" {
				tenant = pool.Tenant
			}
			ts, ok := settings[tenant]
			if !ok {
				ts, err = s.TenantSettings(ctx, poolID, tenant)
				if err != nil {
					return nil, err
				}
				settings[tenant] = ts
			}
			if !ts.SpillOver || ts.SpillAfter <= 0 || inv.CancelRequested {
				continue
			}
			if now.Sub(inv.QueuedAt) < ts.SpillAfter {
				continue
			}
			req, err := decodeRequest(inv)
			if err != nil {
				if ferr := s.failSpill(ctx, inv.ClientRef, core.CodeInvalidArgument, 0); ferr != nil {
					return nil, ferr
				}
				continue
			}
			target := pickRemote(remotes, inv.Model, req.Op, required)
			if target == nil {
				continue
			}
			deadline, owned, err := s.claimSpill(ctx, inv, target.Name, target.Estimate)
			if err != nil {
				return nil, err
			}
			if !owned {
				// A worker leased the row or another replica spilled it.
				continue
			}
			startCtx := ctx
			if inv.Tenant != "" {
				startCtx = core.WithTenant(ctx, inv.Tenant)
			}
			handle, err := target.Invoker.Start(startCtx, req)
			if err != nil {
				// The invocation was never accepted remotely. A definitive
				// refusal costs nothing; an ambiguous one settles the
				// reservation as spent, because the provider may have run.
				charged := int64(0)
				if !definitiveFailure(err) {
					charged = target.Estimate
				}
				if ferr := s.failSpill(ctx, inv.ClientRef, core.CodeOf(err), charged); ferr != nil {
					return nil, ferr
				}
				continue
			}
			if err := s.attachSpillHandle(ctx, inv.ClientRef, handle.ID); err != nil {
				// The claim was lost: the sweep ended the row at its
				// deadline. The remote start must not become a paid orphan.
				_ = target.Invoker.Cancel(context.WithoutCancel(ctx), handle)
				if core.CodeOf(err) == core.CodeConflict {
					continue
				}
				return nil, err
			}
			res, resErr, cancelled := s.awaitSpill(ctx, inv.ClientRef, target, handle, deadline)
			if core.CodeOf(resErr) == CodeLeaseLost || ctx.Err() != nil {
				// The platform already ended the row, or the janitor is
				// shutting down and the claim deadline will settle it.
				continue
			}
			if err := s.finishSpilled(ctx, inv.ClientRef, res, resErr, target.Estimate, cancelled); err != nil {
				return nil, err
			}
			out = append(out, SpillResult{ClientRef: inv.ClientRef, Pool: poolID, Remote: target.Name, Reserved: target.Estimate})
			s.auditEvent(ctx, "worker.spill", poolID, "allowed", target.Name)
		}
	}
	return out, nil
}

// pickRemote returns the first remote that serves the model and operation and
// carries the labels the call requires.
func pickRemote(remotes []Remote, modelID string, op model.Capability, required map[string]string) *Remote {
	for i := range remotes {
		if remotes[i].Invoker == nil || !labelsMatch(remotes[i].Labels, required) {
			continue
		}
		if remotes[i].serves(modelID, op) {
			return &remotes[i]
		}
	}
	return nil
}

// claimSpill moves a waiting invocation to spilling for a remote provider
// before its paid start. The update is the claim: only one caller can take
// the row, and while it is spilling no worker lease can reach it. The claim
// deadline is the pool lease; the sweep fails the row if the remote has not
// reported by then, so a crash never turns into a second paid start.
func (s *Store) claimSpill(ctx context.Context, inv Invocation, remote string, reserved int64) (time.Time, bool, error) {
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return time.Time{}, false, err
	}
	now := s.now()
	deadline := now.Add(pool.Lease)
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET state = ?, spilled = 1, reserved_mpts = ?, worker_id = ?,
			provider_handle = '', lease_expires_unix_ms = ?, leased_unix_ms = ?
		 WHERE client_ref = ? AND state = ? AND cancel_requested = 0`,
		stateSpilling, reserved, remote, unixms(deadline), unixms(now), inv.ClientRef, stateWaiting)
	if err != nil {
		return time.Time{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return time.Time{}, false, err
	}
	return deadline, n == 1, nil
}

// attachSpillHandle records the remote handle on the claimed row.
func (s *Store) attachSpillHandle(ctx context.Context, clientRef, handle string) error {
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET provider_handle = ? WHERE client_ref = ? AND state = ?`,
		handle, clientRef, stateSpilling)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return core.NewError(core.CodeConflict, "the spill claim is no longer held", map[string]any{"client_ref": clientRef})
	}
	return nil
}

// awaitSpill waits for the remote result while the row is claimed. A cancel
// on the row reaches the remote provider and the invocation ends cancelled
// with what the provider reports, never as a charge for work nobody wanted
// (REQ-MODEL-026). The wait ends at the claim deadline, which leaves the row
// spilling for the sweep to settle.
func (s *Store) awaitSpill(ctx context.Context, clientRef string, target *Remote, handle model.Handle, deadline time.Time) (model.Result, error, bool) {
	type outcome struct {
		res model.Result
		err error
	}
	results := make(chan outcome, 1)
	go func() {
		res, err := target.Invoker.Result(ctx, handle)
		results <- outcome{res: res, err: err}
	}()
	cancelled := false
	for {
		select {
		case out := <-results:
			if !cancelled {
				// A cancel that arrived just before the result still ends the
				// invocation cancelled, never as a success.
				if inv, err := s.Invocation(ctx, clientRef); err == nil && inv.CancelRequested {
					cancelled = true
					_ = target.Invoker.Cancel(context.WithoutCancel(ctx), handle)
				}
			}
			return out.res, out.err, cancelled
		case <-s.after(pollInterval):
			inv, err := s.Invocation(ctx, clientRef)
			if err != nil {
				return model.Result{}, errLeaseLost(clientRef), cancelled
			}
			if isTerminal(inv.State) {
				return model.Result{}, errLeaseLost(clientRef), cancelled
			}
			if !cancelled && inv.CancelRequested {
				cancelled = true
				if err := target.Invoker.Cancel(context.WithoutCancel(ctx), handle); err != nil {
					return model.Result{}, err, cancelled
				}
			}
			if !s.now().Before(deadline) {
				return model.Result{}, core.NewError(core.CodeAmbiguousStart,
					"the remote provider did not report before the claim deadline", nil), cancelled
			}
		case <-ctx.Done():
			return model.Result{}, ctx.Err(), cancelled
		}
	}
}

// failSpill records a spill that never reached a result. The invocation is
// failed with the provider's code, never reported as a success.
func (s *Store) failSpill(ctx context.Context, clientRef string, code core.Code, charged int64) error {
	_, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET state = ?, code = ?, charged_mpts = ?, in_doubt = 0,
			in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0, leased_unix_ms = 0,
			cancel_requested = 0
		 WHERE client_ref = ? AND state IN (?, ?)`,
		stateFailed, string(code), charged, clientRef, stateWaiting, stateSpilling)
	return err
}

// finishSpilled writes the remote result onto the claimed row. A remote that
// failed ends failed with its code; only a success becomes stateRemote. A
// cancelled spill ends cancelled and keeps the cost the provider reported.
func (s *Store) finishSpilled(ctx context.Context, clientRef string, res model.Result, resErr error, estimate int64, cancelled bool) error {
	var state string
	code := core.Code("")
	charged := res.CostMpts
	switch {
	case cancelled:
		state = stateCancelled
		code = core.CodeCancelled
		if charged < 0 {
			charged = 0
		}
		if resErr != nil && !definitiveFailure(resErr) && charged < estimate {
			charged = estimate
		}
	case resErr != nil:
		code = core.CodeOf(resErr)
		state = stateFailed
		if code == core.CodeCancelled {
			state = stateCancelled
		}
		if !definitiveFailure(resErr) && charged < estimate {
			charged = estimate
		}
	case res.State == model.StateFailed:
		state = stateFailed
		code = failureCode(res)
	case res.State == model.StateCancelled:
		state = stateCancelled
		code = core.CodeCancelled
	case res.State == model.StateSucceeded || res.State == "":
		state = stateRemote
	default:
		// The provider is still running: the platform cannot report a
		// success it does not have. Settle the reservation as spent.
		state = stateFailed
		code = core.CodeAmbiguousStart
		if charged < estimate {
			charged = estimate
		}
	}
	if charged < 0 {
		charged = 0
	}
	result := res.Message
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	_, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET state = ?, charged_mpts = ?, code = ?, result = ?,
			in_doubt = 0, in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0, leased_unix_ms = 0,
			cancel_requested = 0
		 WHERE client_ref = ? AND state = ?`,
		state, charged, string(code), string(result), clientRef, stateSpilling)
	return err
}

func (s *Store) waiting(ctx context.Context, poolID string) ([]Invocation, error) {
	rows, err := s.db.Execer().Query(ctx, selectInvocation+` WHERE pool_id = ? AND state = ? ORDER BY queued_unix_ms, client_ref`, poolID, stateWaiting)
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
		out = append(out, inv)
	}
	return out, rows.Err()
}

// ExpectedWait estimates how long a new invocation waits in a pool, from the
// queue depth and the units present workers can take (ADR-0012 §8: the catalog
// shows queue depth and expected wait per model).
func (s *Store) ExpectedWait(ctx context.Context, poolID, modelID string) (time.Duration, error) {
	m, err := s.Metrics(ctx, poolID)
	if err != nil {
		return 0, err
	}
	depth := m.QueueDepth[modelID]
	if depth == 0 {
		return 0, nil
	}
	workers, err := s.ListWorkers(ctx, poolID)
	if err != nil {
		return 0, err
	}
	var units int
	for _, w := range workers {
		if !w.Present || w.State != StateActive {
			continue
		}
		for _, d := range w.Descriptors {
			if d.ID == modelID {
				units += w.Capacity - w.UnitsInUse
				break
			}
		}
	}
	per := time.Minute
	if units > 0 {
		per = time.Duration(depth/units+1) * time.Minute
	}
	return per, nil
}
