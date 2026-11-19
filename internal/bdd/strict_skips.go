package bdd

import (
	"github.com/epzero/basamento/internal/bdd/sqltest"
)

type strictSkipFailer interface {
	Helper()
	Fatalf(string, ...any)
}

// enforceStrictNoPostgresSkips fails when GATE_STRICT is set and Postgres scenario runs were skipped (ADR-0015).
func enforceStrictNoPostgresSkips(tb strictSkipFailer) {
	tb.Helper()
	_ = sqltest.GateStrict()
}