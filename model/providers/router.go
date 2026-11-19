package providers

import (
	"context"
	"fmt"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// Profile is the deployment profile a routing decision is made in. The
// embedded profile may use everything on the machine; the cloud profile never
// routes to a machine-local model (REQ-MODEL-011).
type Profile string

const (
	// ProfileCloud is the SaaS profile.
	ProfileCloud Profile = "cloud"
	// ProfileEmbedded is the single-machine profile.
	ProfileEmbedded Profile = "embedded"
	// ProfileLibrary is a process embedding the packages directly.
	ProfileLibrary Profile = "library"
)

// Route is one model a router may choose: its descriptor and the invoker that
// serves it.
type Route struct {
	Invoker    model.Invoker
	Descriptor model.Descriptor
}

// Router chooses a model for a capability and a profile from the providers it
// knows. It is safe for concurrent use.
type Router struct {
	mu     sync.RWMutex
	routes []Route
}

// NewRouter returns a router over routes. A route with no invoker, a
// descriptor with no id, or the same model id served twice is a configuration
// error.
func NewRouter(routes ...Route) (*Router, error) {
	r := &Router{}
	for _, route := range routes {
		if err := r.Add(route); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Add registers one route.
func (r *Router) Add(route Route) error {
	if route.Invoker == nil {
		return core.NewError(core.CodeInvalidArgument, "route has no invoker", nil)
	}
	if route.Descriptor.ID == "" {
		return core.NewError(core.CodeInvalidArgument, "route descriptor has no model id", nil)
	}
	if route.Descriptor.Provider == "" {
		if named, ok := route.Invoker.(interface{ Provider() string }); ok {
			route.Descriptor.Provider = named.Provider()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.routes {
		if existing.Descriptor.ID == route.Descriptor.ID {
			return core.NewError(core.CodeConflict,
				fmt.Sprintf("model %q is already routed", route.Descriptor.ID),
				map[string]any{"model": route.Descriptor.ID})
		}
	}
	r.routes = append(r.routes, route)
	return nil
}

// AddInvoker asks invoker for its descriptors and registers each as a route.
func (r *Router) AddInvoker(ctx context.Context, invoker model.Invoker) error {
	descs, err := invoker.Describe(ctx)
	if err != nil {
		return err
	}
	for _, d := range descs {
		if err := r.Add(Route{Invoker: invoker, Descriptor: d}); err != nil {
			return err
		}
	}
	return nil
}

// Describe lists every model the router may choose.
func (r *Router) Describe(context.Context) ([]model.Descriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]model.Descriptor, 0, len(r.routes))
	for _, route := range r.routes {
		out = append(out, route.Descriptor)
	}
	return out, nil
}

// Pick returns the model the router routes capability to in profile.
//
// A cloud profile never selects a machine-local model (REQ-MODEL-011). Among
// the eligible models, a cloud model wins over a worker model, which wins over
// a machine-local one; ties keep registration order. When the capability
// exists only on models the profile excludes, Pick fails with "unsupported";
// when no model declares it at all, with "not_found".
func (r *Router) Pick(profile Profile, capability model.Capability) (Route, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	found := false
	best := -1
	for i, route := range r.routes {
		if !supports(route.Descriptor, capability) {
			continue
		}
		found = true
		if profile == ProfileCloud && route.Descriptor.Locality == model.LocalityMachineLocal {
			continue
		}
		if best == -1 || localityRank(route.Descriptor.Locality) < localityRank(r.routes[best].Descriptor.Locality) {
			best = i
		}
	}
	if best >= 0 {
		return r.routes[best], nil
	}
	if found {
		return Route{}, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("no model of capability %s is routable in profile %s", capability, profile),
			map[string]any{"op": string(capability), "profile": string(profile)})
	}
	return Route{}, core.NewError(core.CodeNotFound,
		fmt.Sprintf("no model serves capability %s", capability),
		map[string]any{"op": string(capability)})
}

// localityRank prefers cloud capacity over a worker and a worker over the
// machine, so a cloud profile does not pick a local model merely because it
// was registered first.
func localityRank(l model.Locality) int {
	switch l {
	case model.LocalityCloud:
		return 0
	case model.LocalityWorker:
		return 1
	case model.LocalityMachineLocal:
		return 2
	default:
		return 3
	}
}

func supports(d model.Descriptor, capability model.Capability) bool {
	for _, c := range d.Caps {
		if c == capability {
			return true
		}
	}
	return false
}
