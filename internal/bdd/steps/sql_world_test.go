package steps

import (
	"errors"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/messages/go/v34"

	"github.com/epzero/basamento/internal/bdd/sqltest"
)

func scenarioWithTags(names ...string) *godog.Scenario {
	tags := make([]*messages.PickleTag, 0, len(names))
	for _, name := range names {
		tags = append(tags, &messages.PickleTag{Name: name})
	}
	return &godog.Scenario{Tags: tags}
}

// TestIsPostgresPass pins ADR-0015's definition: only the SQL conformance
// driver's Postgres dialect pass counts, identified by its @REQ-SQL tag.
func TestIsPostgresPass(t *testing.T) {
	cases := []struct {
		name string
		scn  *godog.Scenario
		want bool
	}{
		{name: "sql scenario", scn: scenarioWithTags("@REQ-SQL-001", "@profile:cloud", "@id:SQL-001-02"), want: true},
		{name: "inherited requirement tag", scn: scenarioWithTags("@wp:WP-L1-01", "@REQ-SQL-013", "@profile:embedded"), want: true},
		{name: "cloud non-sql scenario", scn: scenarioWithTags("@REQ-BUS-015", "@profile:cloud"), want: false},
		{name: "no tags", scn: &godog.Scenario{}, want: false},
		{name: "nil scenario", scn: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPostgresPass(tc.scn); got != tc.want {
				t.Fatalf("isPostgresPass = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSkipByProfileCountsOnlyPostgresPasses proves a @profile:cloud scenario
// that is not a Postgres pass is skipped without being tallied as one
// (ADR-0015: the report and the strict rule name Postgres runs only).
func TestSkipByProfileCountsOnlyPostgresPasses(t *testing.T) {
	cases := []struct {
		name      string
		tags      []string
		wantSkips int
	}{
		{
			name:      "sql conformance scenario",
			tags:      []string{"@REQ-SQL-015", "@profile:cloud", "@id:SQL-015-01"},
			wantSkips: 1,
		},
		{
			name:      "bus cloud scenario",
			tags:      []string{"@REQ-BUS-015", "@profile:cloud", "@id:BUS-015-01"},
			wantSkips: 0,
		},
		{
			name:      "embedded-only profile skip",
			tags:      []string{"@REQ-SQL-001", "@profile:embedded", "@id:SQL-001-01"},
			wantSkips: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sqltest.ResetPostgresForTest()
			sqltest.ResetSkipCounts()
			err := skipByProfile(scenarioWithTags(tc.tags...), "cloud")
			if !errors.Is(err, godog.ErrSkip) {
				t.Fatalf("want godog.ErrSkip without a server, got %v", err)
			}
			if got := sqltest.SkipTotal(); got != tc.wantSkips {
				t.Fatalf("skips = %d, want %d (%v)", got, tc.wantSkips, sqltest.SkipRuns())
			}
		})
	}
}

// TestSkipByProfileReasonsStayClosed keeps the recorded reason inside the
// closed set the report and summary accept.
func TestSkipByProfileReasonsStayClosed(t *testing.T) {
	sqltest.ResetPostgresForTest()
	sqltest.ResetSkipCounts()
	if err := skipByProfile(scenarioWithTags("@REQ-SQL-001", "@profile:cloud"), "cloud"); !errors.Is(err, godog.ErrSkip) {
		t.Fatalf("want skip, got %v", err)
	}
	for reason := range sqltest.SkipRuns() {
		if !strings.EqualFold(reason, sqltest.SkipPostgresUnavailable) {
			t.Fatalf("unexpected reason %q", reason)
		}
	}
}
