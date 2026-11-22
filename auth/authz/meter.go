package authz

import "context"

// AuditFailureCounter records a failed denial audit write (REQ-AUTH-025).
// A nil counter is a no-op.
type AuditFailureCounter interface {
	Add(context.Context, int64)
}
