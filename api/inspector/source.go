package inspector

import (
	"context"
	"encoding/json"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
)

// RunSummary is one run in the inspector's list: its id, flow and state
// (REQ-INSP-001). Tenant is the scope the handler checks; it is never rendered.
type RunSummary struct {
	ID     string        `json:"id"`
	Flow   string        `json:"flow"`
	State  string        `json:"state"`
	Tenant core.TenantID `json:"-"`
}

// Event is one durable run event in sequence order (REQ-INSP-004).
type Event struct {
	Seq  int64           `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Gate is one open gate: its node id and the payload expression it accepts
// (REQ-INSP-005).
type Gate struct {
	Node    string `json:"node"`
	Accepts string `json:"accepts"`
}

// Recording is the rrweb recording of a run's browser session
// (docs/architecture/browser-runner.md §3). A nil *Recording means the run has
// none (REQ-INSP-011); Err marks one that cannot be loaded (REQ-INSP-012).
type Recording struct {
	Frames []json.RawMessage
	Err    bool
}

// State reports how the inspector must present the recording: "present",
// "none" or "error".
func (r *Recording) State() string {
	switch {
	case r == nil:
		return "none"
	case r.Err:
		return "error"
	default:
		return "present"
	}
}

// Run is one run's detail. Tenant and Project are the server-side scope of the
// record; they are never rendered.
type Run struct {
	ID        string
	Flow      string
	State     string
	ErrorCode string
	Gates     []Gate
	Recording *Recording
	Tenant    core.TenantID
	Project   string
}

// Source is the tenant-scoped read model the inspector renders. The API of
// WP-L6-03 implements it at the edge: every method receives the authenticated
// principal, takes the tenant from that principal (never from a request
// field) and answers a run of another tenant as not_found, so the inspector
// cannot tell it apart from a missing one (REQ-INSP-002). A missing read
// permission is permission_denied (REQ-INSP-013); a rejected gate payload is
// invalid_argument with the API's reason (REQ-INSP-007).
//
// Run and List must set Tenant on every record they return. The handler fails
// closed when a record's Tenant differs from the principal's.
type Source interface {
	// List returns the runs of the principal's projects it may read.
	List(ctx context.Context, principal authn.Principal) ([]RunSummary, error)
	// Run returns one run's state, open gates and recording.
	Run(ctx context.Context, principal authn.Principal, runID string) (Run, error)
	// Events returns the durable events after seq, in sequence order. After 0
	// is the stored history.
	Events(ctx context.Context, principal authn.Principal, runID string, after int64) ([]Event, error)
	// Resolve submits payload to one open gate. It validates the payload and
	// returns invalid_argument when the gate does not accept it.
	Resolve(ctx context.Context, principal authn.Principal, runID, gate string, payload json.RawMessage) error
}
