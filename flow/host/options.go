package host

import "github.com/epzero/basamento/flow/machine"

// Option configures a Host at construction.
type Option func(*Host)

// WithMaxFanout lowers the map fan-out limit for every machine the host starts
// (REQ-FLOW-050, REQ-FLOW-060). Values above the default are clamped. Values
// below 1 do not fail here; each run fails with invalid_argument before any
// durable call (REQ-FLOW-061).
func WithMaxFanout(limit int) Option {
	return func(h *Host) {
		h.machineOpts = append(h.machineOpts, machine.WithMaxFanout(limit))
	}
}
