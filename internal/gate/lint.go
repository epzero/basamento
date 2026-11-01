package gate

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Lint checks the spec rules of docs/engineering/bdd-convention.md §4.
func Lint(s *Spec, today time.Time) []string {
	errs := append([]string{}, s.ParseErrors...)
	seen := map[string]string{}
	for _, sc := range s.Scenarios {
		at := fmt.Sprintf("%s:%d %q", sc.File, sc.Line, sc.Name)
		switch {
		case sc.ID == "":
			errs = append(errs, at+": missing scenario-level @id:<CAP-NNN-NN> tag")
		case !scenarioID.MatchString(sc.ID):
			errs = append(errs, fmt.Sprintf("%s: @id:%s must match CAP-NNN-NN", at, sc.ID))
		default:
			if prev, ok := seen[sc.ID]; ok {
				errs = append(errs, fmt.Sprintf("%s: duplicate @id:%s (also %s)", at, sc.ID, prev))
			}
			seen[sc.ID] = at
			if primary := "REQ-" + sc.ID[:strings.LastIndex(sc.ID, "-")]; !contains(sc.Reqs, primary) {
				errs = append(errs, fmt.Sprintf("%s: @id:%s names %s as its primary requirement; tag the scenario @%s or renumber it", at, sc.ID, primary, primary))
			}
		}
		if len(sc.Reqs) == 0 {
			errs = append(errs, at+": no @REQ-* tag")
		}
		for _, r := range sc.Reqs {
			if _, ok := s.Requirements[r]; !ok {
				errs = append(errs, fmt.Sprintf("%s: @%s is not defined in any requirements.md", at, r))
			} else if s.Retired[r] {
				errs = append(errs, fmt.Sprintf("%s: @%s is retired; delete the scenario or point it at a live requirement", at, r))
			}
		}
		if sc.Steps > maxSteps {
			errs = append(errs, fmt.Sprintf("%s: %d steps (max %d); split the behavior", at, sc.Steps, maxSteps))
		}
		if sc.IsPlanned && sc.WP == "" {
			errs = append(errs, at+": @planned needs @wp:<WP-…> naming the work package that implements it")
		}
		if sc.WP != "" && !s.WorkPackages[sc.WP] {
			errs = append(errs, fmt.Sprintf("%s: @wp:%s is not a work package in docs/roadmap/", at, sc.WP))
		}
		errs = append(errs, lintTags(at, sc, today)...)
	}
	sort.Strings(errs)
	return errs
}

func lintTags(at string, sc *Scenario, today time.Time) []string {
	var errs []string
	quarantined, until, issue := false, "", false
	for _, t := range sc.Tags {
		if bannedTags[t] {
			errs = append(errs, fmt.Sprintf("%s: tag %s is not allowed on main", at, t))
			continue
		}
		if simpleTags[t] || reqTag.MatchString(t) {
			if t == "@quarantine" {
				quarantined = true
			}
			continue
		}
		known := false
		for _, p := range prefixTags {
			if strings.HasPrefix(t, p) {
				known = true
				if vals, ok := enumTags[p]; ok && !contains(vals, strings.TrimPrefix(t, p)) {
					errs = append(errs, fmt.Sprintf("%s: %s must be one of %s", at, t, strings.Join(vals, ",")))
				}
			}
		}
		if m := untilTag.FindStringSubmatch(t); m != nil {
			until = m[1]
		}
		if issueTag.MatchString(t) {
			issue = true
		}
		if !known {
			errs = append(errs, fmt.Sprintf("%s: unknown tag %s (see bdd-convention.md §4.3)", at, t))
		}
	}
	if quarantined {
		if !issue || until == "" {
			errs = append(errs, at+": @quarantine needs @issue:<key> and @until:<YYYY-MM-DD>")
		} else if _, err := time.Parse("2006-01-02", until); err != nil || today.UTC().Format("2006-01-02") > until {
			// The quarantine holds through its @until day and expires the day after.
			errs = append(errs, fmt.Sprintf("%s: quarantine expired on %s; fix or delete via spec-change", at, until))
		}
	}
	return errs
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// TraceRow is one requirement with its scenarios.
type TraceRow struct {
	Req       string   `json:"req"`
	File      string   `json:"file"`
	Scenarios []string `json:"scenarios"`
	Planned   []string `json:"planned"`
}

// Trace builds the requirement→scenario matrix and reports requirements without scenarios.
func Trace(s *Spec) ([]TraceRow, []string) {
	rows := map[string]*TraceRow{}
	for id, f := range s.Requirements {
		rows[id] = &TraceRow{Req: id, File: f}
	}
	for _, sc := range s.Scenarios {
		for _, r := range sc.Reqs {
			if row, ok := rows[r]; ok {
				if sc.IsPlanned {
					row.Planned = append(row.Planned, sc.ID)
				} else {
					row.Scenarios = append(row.Scenarios, sc.ID)
				}
			}
		}
	}
	var out []TraceRow
	var errs []string
	for _, r := range rows {
		if len(r.Scenarios)+len(r.Planned) == 0 && !s.Retired[r.Req] {
			errs = append(errs, fmt.Sprintf("%s (%s): no scenario covers this requirement", r.Req, r.File))
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Req < out[j].Req })
	sort.Strings(errs)
	return out, errs
}
