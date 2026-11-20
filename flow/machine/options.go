package machine

import (
	"github.com/epzero/basamento/core"
)

// DefaultMaxFanout is the largest map fan-out the machine accepts in one
// superstep. A model or set node can put a large array in state, and the
// machine would emit one command per element; max_parallel is the host's
// throttle, not a cap on how many commands a map may produce.
const DefaultMaxFanout = 10000

// DefaultMaxPureSupersteps is the floor for consecutive command-free supersteps
// inside one advance call. The effective limit also scales with the definition's
// declared max_visits budgets (WithMaxPureSupersteps can raise the floor).
const DefaultMaxPureSupersteps = 100_000

// Option configures a Machine at construction.
type Option func(*Machine) error

// WithMaxFanout sets the map fan-out limit. The host may lower it but never
// raise it above DefaultMaxFanout. Values below 1 are stored as given; the
// machine rejects them when a run starts (REQ-FLOW-061).
func WithMaxFanout(n int) Option {
	return func(m *Machine) error {
		if n > DefaultMaxFanout {
			n = DefaultMaxFanout
		}
		m.maxFanout = n
		return nil
	}
}

// WithMaxPureSupersteps sets the floor for consecutive command-free supersteps
// within one advance. n must be at least 1.
func WithMaxPureSupersteps(n int) Option {
	return func(m *Machine) error {
		if n < 1 {
			return core.NewError(core.CodeInvalidArgument, "flow: max pure supersteps must be at least 1", nil)
		}
		m.maxPureSupersteps = n
		return nil
	}
}
