package host

import "github.com/epzero/basamento/flow/machine"

// Option configures a Host at construction.
type Option func(*Host)

// WithMaxFanout lowers the map fan-out limit for every machine the host starts
// (REQ-FLOW-050, REQ-FLOW-060). Values above machine.DefaultMaxFanout are
// clamped to it. Values below 1 make each run fail with invalid_argument when
// the host builds the machine for the run, before any durable call
// (REQ-FLOW-061).
func WithMaxFanout(limit int) Option {
	return func(h *Host) {
		h.machineOpts = append(h.machineOpts, machine.WithMaxFanout(limit))
	}
}
