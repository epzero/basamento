package host

import "github.com/epzero/basamento/flow/machine"

// Option configures a Host at construction.
type Option func(*Host)

// WithMaxFanout lowers the map fan-out limit for every machine the host starts
// (REQ-FLOW-050). Values below 1 are rejected when the run starts, before any
// durable call.
func WithMaxFanout(limit int) Option {
	return func(h *Host) {
		h.machineOpts = append(h.machineOpts, machine.WithMaxFanout(limit))
	}
}
