package credentials

import "context"

// Use is who resolves a credential, and where. The step host sets it on the
// context at the moment of use; the store does not authenticate the caller and
// audits exactly these fields (REQ-CRED-017). Empty fields are audited empty.
type Use struct {
	// Principal is human:<login> or <harness>:<role>:<instance>.
	Principal string
	// RunID is the run the step belongs to, empty outside a run.
	RunID string
	// StepID is the step that resolves the credential, empty outside a step.
	StepID string
}

type useKey struct{}

// WithUse returns a context carrying u. A later call replaces it; the parent
// context is unchanged.
func WithUse(ctx context.Context, u Use) context.Context {
	return context.WithValue(ctx, useKey{}, u)
}

// UseFrom returns the use carried by ctx, or an empty Use when there is none.
// A missing principal, run or step reads as an empty string.
func UseFrom(ctx context.Context) Use {
	if ctx == nil {
		return Use{}
	}
	u, _ := ctx.Value(useKey{}).(Use)
	return u
}
