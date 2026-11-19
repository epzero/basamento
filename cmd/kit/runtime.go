package main

import (
	"path/filepath"

	"github.com/epzero/basamento/internal/config"
	"github.com/epzero/basamento/runtime/temporal/embedded"
)

// runtimeProcess is the durable runtime a profile selects: the Temporal server
// the embedded profile runs in-process, or the external service the cloud
// profile points at. close releases whatever it started.
type runtimeProcess struct {
	name           string
	implementation string
	hostPort       string
	stop           func()
}

// startRuntime starts the durable runtime for the profile at its place in the
// start-up order: after the ports, because a data directory the ports cannot
// use must still be reported as the port that failed (CONFIG-021), and before
// the listener, so the ready line names a runtime that is running.
//
// The embedded profile starts the in-process Temporal server on a file-backed
// SQLite database under data.dir (REQ-RT-003, WP-L5-03). The cloud profile
// talks to a Temporal service the operator runs; this binary starts nothing
// for it.
func startRuntime(cfg config.Config) (*runtimeProcess, error) {
	if cfg.Profile == config.ProfileCloud {
		return &runtimeProcess{name: "temporal", implementation: "cloud"}, nil
	}
	srv, err := embedded.Start(embedded.Options{DBFile: filepath.Join(cfg.Data.Dir, "temporal.db")})
	if err != nil {
		return nil, err
	}
	return &runtimeProcess{
		name:           "temporal",
		implementation: "embedded",
		hostPort:       srv.HostPort,
		stop:           srv.Stop,
	}, nil
}

// close stops the runtime. It is safe on a nil process.
func (r *runtimeProcess) close() {
	if r != nil && r.stop != nil {
		r.stop()
	}
}
