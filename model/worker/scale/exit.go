package scale

import "time"

// ExitPolicy is a launched worker's own reason to leave. The platform never
// kills a worker, because it cannot know what a kill would cost; the worker
// leaves when idle, after its maximum lifetime, or after a maximum number of
// invocations (ADR-0012 §12, REQ-MODEL-042).
type ExitPolicy struct {
	// IdleExit is how long a worker may hold no lease before leaving.
	IdleExit time.Duration
	// MaxLifetime is how long a worker may run in total.
	MaxLifetime time.Duration
	// MaxInvocations is how many invocations a worker may run; 0 means no
	// limit.
	MaxInvocations int
}

// Leave reports whether a worker that started at started, last held work at
// lastWork and has run invocations invocations should leave at now.
func (p ExitPolicy) Leave(now, started, lastWork time.Time, invocations int) bool {
	if p.MaxInvocations > 0 && invocations >= p.MaxInvocations {
		return true
	}
	if p.MaxLifetime > 0 && !started.IsZero() && !now.Before(started.Add(p.MaxLifetime)) {
		return true
	}
	if p.IdleExit > 0 {
		if lastWork.IsZero() {
			lastWork = started
		}
		if !lastWork.IsZero() && !now.Before(lastWork.Add(p.IdleExit)) {
			return true
		}
	}
	return false
}
