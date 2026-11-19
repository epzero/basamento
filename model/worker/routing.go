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
	// Estimate is the reservation for one invocation, in milli-points.
	Estimate int64
}

func (r Remote) serves(modelID string) bool {
	for _, d := range r.Descriptors {
		if d.ID == modelID {
			return true
		}
	}
	return false
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
// model (REQ-MODEL-031) and honours required labels.
type Router struct {
	store   *Store
	remotes []Remote
}

// NewRouter builds a router over a worker store and the other providers of
// the deployment.
func NewRouter(store *Store, remotes ...Remote) *Router {
	return &Router{store: store, remotes: remotes}
}

// Select routes one invocation. A tenant's own pool wins over a shared pool
// and either wins over another provider; within the pool, the worker labels
// the call requires must be served by a present worker.
func (r *Router) Select(ctx context.Context, tenant core.TenantID, modelID string) (Destination, error) {
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
		if _, err := r.store.descriptorFor(ctx, id, modelID); err != nil {
			if core.CodeOf(err) == core.CodeNotFound {
				continue
			}
			return Destination{}, err
		}
		if len(required) > 0 {
			ok, err := r.store.poolHasLabels(ctx, id, modelID, required)
			if err != nil {
				return Destination{}, err
			}
			if !ok {
				continue
			}
		}
		if pool.Tenant != "" && pool.Tenant == tenant {
			d, err := r.store.descriptorFor(ctx, id, modelID)
			return Destination{Pool: id, Model: d.Descriptor, Tenant: tenant}, err
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
		if r.remotes[i].serves(modelID) {
			remote := r.remotes[i]
			return Destination{Remote: &remote, IsRemote: true, Tenant: tenant}, nil
		}
	}
	return Destination{}, core.NewError(core.CodeNotFound,
		fmt.Sprintf("no provider serves model %q", modelID), map[string]any{"model": modelID})
}

func (s *Store) poolHasLabels(ctx context.Context, poolID, modelID string, labels map[string]string) (bool, error) {
	workers, err := s.ListWorkers(ctx, poolID)
	if err != nil {
		return false, err
	}
	for _, w := range workers {
		if !w.Present || w.Removed || !labelsMatch(w.Labels, labels) {
			continue
		}
		for _, d := range w.Descriptors {
			if d.ID == modelID {
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
// provider of the model once it has waited their spill-over time. The
// reservation is made at the other provider's price before its start
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
			var target *Remote
			for i := range remotes {
				if remotes[i].serves(inv.Model) {
					target = &remotes[i]
					break
				}
			}
			if target == nil {
				continue
			}
			if err := s.markSpilled(ctx, inv.ClientRef, target.Name, target.Estimate); err != nil {
				return nil, err
			}
			result := SpillResult{ClientRef: inv.ClientRef, Pool: poolID, Remote: target.Name, Reserved: target.Estimate}
			out = append(out, result)
			req := model.Request{Model: inv.Model, ClientRef: inv.ClientRef}
			if len(inv.Request) > 0 {
				var payload map[string]any
				if err := json.Unmarshal(inv.Request, &payload); err == nil {
					req.Params = inv.Request
				}
			}
			if target.Invoker != nil {
				if _, err := target.Invoker.Start(ctx, req); err != nil {
					return out, err
				}
			}
			s.auditEvent(ctx, "worker.spill", poolID, "allowed", target.Name)
		}
	}
	return out, nil
}

func (s *Store) waiting(ctx context.Context, poolID string) ([]Invocation, error) {
	rows, err := s.db.Execer().Query(ctx, selectInvocation+` WHERE pool_id = ? AND state = ? ORDER BY queued_unix_ms, client_ref`, poolID, stateWaiting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func (s *Store) markSpilled(ctx context.Context, clientRef, remote string, reserved int64) error {
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET state = ?, spilled = 1, reserved_mpts = ?, worker_id = ?
		 WHERE client_ref = ? AND state = ?`,
		stateRemote, reserved, remote, clientRef, stateWaiting)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.NewError(core.CodeConflict, "invocation is no longer waiting", map[string]any{"client_ref": clientRef})
	}
	return nil
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
