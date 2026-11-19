package bdd

import (
	"fmt"
	"testing"

	"github.com/epzero/basamento/internal/bdd/sqltest"
)

type captureFatal struct {
	testing.TB
	msg string
}

func (c *captureFatal) Fatalf(format string, args ...any) {
	c.msg = fmt.Sprintf(format, args...)
}

func TestEnforceStrictNoPostgresSkips(t *testing.T) {
	t.Run("non-strict with skips ok", func(t *testing.T) {
		sqltest.ResetSkipCounts()
		t.Setenv("GATE_STRICT", "")
		sqltest.RecordSkipRun(sqltest.SkipPostgresUnavailable)
		enforceStrictNoPostgresSkips(t)
	})
	t.Run("strict zero skips ok", func(t *testing.T) {
		sqltest.ResetSkipCounts()
		t.Setenv("GATE_STRICT", "1")
		enforceStrictNoPostgresSkips(t)
	})
	t.Run("strict with skips fails", func(t *testing.T) {
		sqltest.ResetSkipCounts()
		t.Setenv("GATE_STRICT", "1")
		sqltest.RecordSkipRun(sqltest.SkipPostgresUnavailable)
		c := &captureFatal{TB: t}
		enforceStrictNoPostgresSkips(c)
		if c.msg == "" {
			t.Fatal("expected Fatalf when strict and skips recorded")
		}
	})
}