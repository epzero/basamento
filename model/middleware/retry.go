package middleware

import (
	"errors"
	"math/rand/v2"
	"time"

	"github.com/epzero/basamento/core"
)

// maxBackoff caps the base wait so a long-running call cannot overflow a
// duration. It is far above any call total timeout the platform sets.
const maxBackoff = time.Hour

// retryWait returns the wait before retry number k (zero-based) of a call: a
// base wait that starts at one second and doubles, plus up to half of it drawn
// from the call's seeded randomness, or the longer Retry-After (REQ-MODEL-051,
// REQ-MODEL-052).
func (e *engine) retryWait(err error, retry int) time.Duration {
	base := time.Second
	for i := 0; i < retry && base < maxBackoff; i++ {
		base *= 2
	}
	jitter := int64(0)
	if e.cfg.Rand != nil {
		limit := int64(base/2) + 1
		jitter = e.cfg.Rand.Int64n(limit)
	}
	if jitter < 0 {
		jitter = 0
	}
	if max := int64(base / 2); jitter > max {
		jitter = max
	}
	wait := base + time.Duration(jitter)
	var pe *core.Error
	if errors.As(err, &pe) && pe.RetryAfter > wait {
		wait = pe.RetryAfter
	}
	return wait
}

type zeroRand struct{}

func (zeroRand) Int64n(int64) int64 { return 0 }

// ZeroRand is the deterministic randomness source that draws no jitter.
func ZeroRand() Rand { return zeroRand{} }

// Seed returns a seeded source for jitter (REQ-MODEL-051). The same seed
// gives the same waits.
func Seed(seed uint64) Rand {
	return seeded{source: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

type seeded struct {
	source *rand.Rand
}

func (s seeded) Int64n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return s.source.Int64N(n)
}
