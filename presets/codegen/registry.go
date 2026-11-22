package codegen

import (
	"fmt"

	"github.com/epzero/basamento/core"
)

// VersionStatus is a version's state in the lifecycle.
type VersionStatus string

// The version states. A version starts proposed, may be rejected, then waits at
// its review gate, and becomes accepted and current or rejected by a person.
const (
	StatusProposed   VersionStatus = "proposed"
	StatusWaiting    VersionStatus = "waiting"
	StatusAccepted   VersionStatus = "accepted"
	StatusSuperseded VersionStatus = "superseded"
	StatusRejected   VersionStatus = "rejected"
)

// Version is one preset version and its recorded per-case eval scores.
type Version struct {
	ID       string
	Sources  Sources
	Scores   map[string]int
	Status   VersionStatus
	Code     core.Code
	Case     string
	GateOpen bool
}

// Registry is the preset version lifecycle (specs/presets/design.md §2): a new
// version is accepted only when it compiles, its @eval suite does not regress
// and a person resolves its review gate. Rejected versions stay recorded.
type Registry struct {
	Suite    []string // eval case ids, in fixture order
	Current  string
	Versions map[string]*Version
	EvalRuns int // cases the last proposal ran
}

// NewRegistry returns an empty registry over the suite's case ids.
func NewRegistry(suite []string) *Registry {
	return &Registry{Suite: append([]string(nil), suite...), Versions: map[string]*Version{}}
}

// Add records a version with its recorded case scores. A version already in
// the registry keeps its recorded lifecycle: a prior rejection is never erased
// by a re-run.
func (r *Registry) Add(id string, sources Sources, scores map[string]int) *Version {
	if v, ok := r.Versions[id]; ok {
		return v
	}
	v := &Version{ID: id, Sources: sources, Scores: scores, Status: StatusProposed}
	r.Versions[id] = v
	return v
}

// AcceptInitial makes id the accepted, current version. It is how a scenario
// starts the lifecycle with an accepted predecessor. A recorded rejection is
// durable and is not overwritten.
func (r *Registry) AcceptInitial(id string) *Version {
	v := r.Versions[id]
	if v == nil {
		v = r.Add(id, DefaultSources(), map[string]int{})
	}
	if v.Status == StatusRejected {
		return v
	}
	v.Status = StatusAccepted
	r.Current = id
	return v
}

// Propose runs the version lifecycle's compile and eval steps. A version that
// does not compile is rejected with invalid_argument before any eval case runs
// and its review gate is never opened. A version whose score is lower than its
// predecessor's on any case, or that has no recorded score for a case its
// predecessor scored, is rejected with conflict naming that case. A missing
// predecessor score also blocks acceptance. Otherwise the version waits at its
// review gate. A rejected or accepted version is returned unchanged: a
// re-run does not silently re-propose it.
func (r *Registry) Propose(id string) *Version {
	v := r.Versions[id]
	if v == nil {
		v = r.Add(id, DefaultSources(), map[string]int{})
	}
	r.EvalRuns = 0
	switch v.Status {
	case StatusRejected, StatusAccepted, StatusSuperseded, StatusWaiting:
		return v
	}
	v.GateOpen = false
	v.Code = ""
	v.Case = ""
	if err := v.Sources.Compile(); err != nil {
		v.Status = StatusRejected
		v.Code = core.CodeInvalidArgument
		return v
	}
	for _, caseID := range r.Suite {
		if _, ok := v.Scores[caseID]; ok {
			r.EvalRuns++
		}
	}
	prev := r.Versions[r.Current]
	for _, caseID := range r.Suite {
		score, ok := v.Scores[caseID]
		if !ok {
			v.Status = StatusRejected
			v.Code = core.CodeConflict
			v.Case = caseID
			return v
		}
		if prev != nil && prev != v {
			before, scored := prev.Scores[caseID]
			if !scored {
				v.Status = StatusRejected
				v.Code = core.CodeConflict
				v.Case = caseID
				return v
			}
			if score < before {
				v.Status = StatusRejected
				v.Code = core.CodeConflict
				v.Case = caseID
				return v
			}
		}
	}
	v.Status = StatusWaiting
	v.GateOpen = true
	return v
}

// Resolve records a person's decision on a waiting version. The decision is
// validated before any state changes, so an unknown decision is an error and
// leaves the gate open. An approved decision makes the version accepted and
// current and supersedes the predecessor; a rejected decision rejects it with
// cancelled and leaves the predecessor current.
func (r *Registry) Resolve(id, decision string) (*Version, error) {
	switch decision {
	case "approved", "rejected":
	default:
		return r.Versions[id], invalid("codegen: unknown review decision %q; use approved or rejected", decision)
	}
	v := r.Versions[id]
	if v == nil {
		return nil, core.NewError(core.CodeNotFound, fmt.Sprintf("codegen: no version %q", id), nil)
	}
	if !v.GateOpen || v.Status != StatusWaiting {
		return v, core.NewError(core.CodeConflict, fmt.Sprintf("codegen: version %q is not waiting at its review gate", id), nil)
	}
	v.GateOpen = false
	switch decision {
	case "approved":
		if prev := r.Versions[r.Current]; prev != nil && prev != v {
			prev.Status = StatusSuperseded
		}
		v.Status = StatusAccepted
		r.Current = id
	case "rejected":
		v.Status = StatusRejected
		v.Code = core.CodeCancelled
	}
	return v, nil
}

// Get returns a recorded version.
func (r *Registry) Get(id string) *Version { return r.Versions[id] }

// Describe renders a version for an error or a log line.
func (v *Version) Describe() string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s (%s)", v.ID, v.Status)
}
