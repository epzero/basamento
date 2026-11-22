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
// provider of the model once it has waited their spill-over time. The remote
// start is made before the row leaves the queue, so a failed start is recorded
// as a failure rather than a success, and the remote result is carried back
// (REQ-MODEL-032, REQ-MODEL-033). Off stays off: a tenant with spill-over off
// never has work moved.
func (s *Store) Spill(ctx context.Context, remotes ...Remote) ([]SpillResult, error) {
	pools, err := s.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []SpillResult
	for _, poolID := range pools {
		pool, err := s.Pool(ctx, poolID)
		if err != nil {
			return nil, err
		}
		if pool.Tenant == "" {
			continue
		}
		settings, err := s.TenantSettings(ctx, poolID, pool.Tenant)
		if err != nil {
			return nil, err
		}
		if !settings.SpillOver || settings.SpillAfter <= 0 {
			continue
		}
		waiting, err := s.waiting(ctx, poolID)
		if err != nil {
			return nil, err
		}
		for _, inv := range waiting {
			if now.Sub(inv.QueuedAt) < settings.SpillAfter {
				continue
			}
			req, err := decodeRequest(inv)
			if err != nil {
				if ferr := s.failSpill(ctx, inv.ClientRef, core.CodeInvalidArgument, 0); ferr != nil {
					return nil, ferr
				}
				continue
			}
			var target *Remote
			for i := range remotes {
				if remotes[i].Invoker != nil && remotes[i].serves(inv.Model, req.Op) {
					target = &remotes[i]
					break
				}
			}
			if target == nil {
				continue
			}
			// Reserve before the paid start so a crash between the two still
			// leaves the charge visible (REQ-MODEL-033).
			owned, err := s.reserveSpill(ctx, inv.ClientRef, target.Estimate)
			if err != nil {
				return nil, err
			}
			if !owned {
				continue
			}
			handle, err := target.Invoker.Start(ctx, req)
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
			if err := s.markSpilled(ctx, inv, target.Name, target.Estimate, handle.ID); err != nil {
				// The row left the queue between the read and the claim; the
				// remote start we just made must not become a paid orphan.
				_ = target.Invoker.Cancel(ctx, handle)
				if core.CodeOf(err) == core.CodeConflict {
					continue
				}
				return nil, err
			}
			res, resErr := target.Invoker.Result(ctx, handle)
			if err := s.finishSpilled(ctx, inv.ClientRef, res, resErr, target.Estimate); err != nil {
				return nil, err
			}
			out = append(out, SpillResult{ClientRef: inv.ClientRef, Pool: poolID, Remote: target.Name, Reserved: target.Estimate})
			s.auditEvent(ctx, "worker.spill", poolID, "allowed", target.Name)
		}
	}
	return out, nil
}

// reserveSpill records a remote provider's reservation on a waiting row
// before the start. It reports whether the row was still waiting: another
// replica may have leased or spilled it in the meantime.
func (s *Store) reserveSpill(ctx context.Context, clientRef string, reserved int64) (bool, error) {
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET reserved_mpts = ? WHERE client_ref = ? AND state = ?`,
		reserved, clientRef, stateWaiting)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// markSpilled claims a waiting invocation for a remote provider after that
// provider accepted the start. The row is not terminal yet: finishSpilled
// records the remote result. The claim deadline keeps a crashed spill from
// being retried as a second paid start.
func (s *Store) markSpilled(ctx context.Context, inv Invocation, remote string, reserved int64, handle string) error {
	pool, err := s.Pool(ctx, inv.Pool)
	if err != nil {
		return err
	}
	now := s.now()
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET state = ?, spilled = 1, reserved_mpts = ?, worker_id = ?,
			provider_handle = ?, lease_expires_unix_ms = ?, leased_unix_ms = ?
		 WHERE client_ref = ? AND state = ?`,
		stateSpilling, reserved, remote, handle, unixms(now.Add(pool.Lease)), unixms(now),
		inv.ClientRef, stateWaiting)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.NewError(core.CodeConflict, "invocation is no longer waiting", map[string]any{"client_ref": inv.ClientRef})
	}
	return nil
}

// failSpill records a spill whose remote start was refused. The invocation is
// failed with the provider's code, never reported as a success.
func (s *Store) failSpill(ctx context.Context, clientRef string, code core.Code, charged int64) error {
	_, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET state = ?, code = ?, charged_mpts = ?, in_doubt = 0,
			in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0, leased_unix_ms = 0
		 WHERE client_ref = ? AND state = ?`,
		stateFailed, string(code), charged, clientRef, stateWaiting)
	return err
}

// finishSpilled writes the remote result onto the claimed row. A remote that
// failed or was cancelled ends failed or cancelled with its code; only a
// success becomes stateRemote.
func (s *Store) finishSpilled(ctx context.Context, clientRef string, res model.Result, resErr error, estimate int64) error {
	var state string
	code := core.Code("")
	charged := res.CostMpts
	switch {
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
			in_doubt = 0, in_doubt_until_unix_ms = 0, lease_expires_unix_ms = 0, leased_unix_ms = 0
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
