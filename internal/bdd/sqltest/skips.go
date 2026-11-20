package sqltest

import (
	"fmt"
	"sync"
)

// SkipPostgresUnavailable is the closed skip reason when the Postgres server cannot run.
const SkipPostgresUnavailable = "postgres-unavailable"

var (
	skipMu     sync.Mutex
	skipCounts map[string]int
)

// ResetSkipCounts clears per-run skip tallies; tests call this in setup.
func ResetSkipCounts() {
	skipMu.Lock()
	skipCounts = nil
	skipMu.Unlock()
}

// RecordSkipRun accounts one skipped Postgres dialect run for reason.
func RecordSkipRun(reason string) {
	if reason != SkipPostgresUnavailable {
		panic(fmt.Sprintf("bdd: unknown skip reason %q", reason))
	}
	skipMu.Lock()
	if skipCounts == nil {
		skipCounts = make(map[string]int)
	}
	skipCounts[reason]++
	skipMu.Unlock()
}

// SkipRuns returns a copy of skip counts keyed by reason, sorted by reason.
func SkipRuns() map[string]int {
	skipMu.Lock()
	defer skipMu.Unlock()
	if len(skipCounts) == 0 {
		return nil
	}
	out := make(map[string]int, len(skipCounts))
	for k, v := range skipCounts {
		out[k] = v
	}
	return out
}

// SkipTotal is the sum of per-reason skip counts.
func SkipTotal() int {
	skipMu.Lock()
	defer skipMu.Unlock()
	n := 0
	for _, v := range skipCounts {
		n += v
	}
	return n
}
