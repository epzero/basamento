package machine

import (
	"github.com/epzero/basamento/core"
)

// DefaultMaxFanout is the largest map fan-out the machine accepts in one
// superstep. A model or set node can put a large array in state, and the
// machine would emit one command per element; max_parallel is the host's
// throttle, not a cap on how many commands a map may produce.
const DefaultMaxFanout = 10000

// Option configures a Machine at construction.
type Option func(*Machine) error

// WithMaxFanout sets the map fan-out limit. The host may lower it; n must be
// at least 1.
func WithMaxFanout(n int) Option {
	return func(m *Machine) error {
		if n < 1 {
			return core.NewError(core.CodeInvalidArgument, "flow: max fanout must be at least 1", nil)
		}
		m.maxFanout = n
		return nil
	}
}