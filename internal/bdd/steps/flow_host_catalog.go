package steps

import (
	"context"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

// --- definition catalog ----------------------------------------------------

type hostCatalog struct {
	mu     sync.RWMutex
	byHash map[string]*flow.IR
	latest map[string]*flow.IR
	// pinSubstitute serves a different definition when a run pins hash but the
	// catalog is configured to return another version (FLOW-054-01).
	pinSubstitute map[string]*flow.IR
}

func newHostCatalog() *hostCatalog {
	return &hostCatalog{
		byHash:        map[string]*flow.IR{},
		latest:        map[string]*flow.IR{},
		pinSubstitute: map[string]*flow.IR{},
	}
}

func (c *hostCatalog) register(def *flow.IR) runtime.FlowRef {
	hash, err := ir.Hash(def)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byHash[hash] = def
	c.latest[def.Name] = def
	return runtime.FlowRef{Name: def.Name, Hash: hash}
}

func (c *hostCatalog) Load(_ context.Context, ref runtime.FlowRef) (*flow.IR, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ref.Hash != "" {
		if sub := c.pinSubstitute[ref.Hash]; sub != nil {
			return sub, nil
		}
		def, ok := c.byHash[ref.Hash]
		if !ok {
			return nil, core.NewError(core.CodeNotFound, "definition not found", nil)
		}
		return def, nil
	}
	def, ok := c.latest[ref.Name]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "definition not found", nil)
	}
	return def, nil
}
