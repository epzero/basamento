package gate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LockFile is one capability's approval ledger: specs/<capability>/spec.lock.yaml.
// One file per capability keeps parallel work from colliding on a shared file.
type LockFile struct {
	Version   int                   `yaml:"version"`
	Scenarios map[string]*LockEntry `yaml:"scenarios"`
}

// LockEntry records which exact scenario text was approved, by whom, at which
// tier, and whether a person has reviewed it yet. Review discussion itself is
// transient (pull request or issue comments); only the decision is kept.
type LockEntry struct {
	Hash       string   `yaml:"hash"`
	Reqs       []string `yaml:"reqs"`
	Tier       string   `yaml:"tier"` // P0 (critical) .. P3 (editorial)
	Author     string   `yaml:"author"`
	Reviewer   string   `yaml:"reviewer"`
	ApprovedAt string   `yaml:"approved_at"`
	Note       string   `yaml:"note,omitempty"` // why this tier; required for P0 and P1
	Ref        string   `yaml:"ref,omitempty"`  // optional link to the review discussion
	Human      Human    `yaml:"human"`
}

// Human is the asynchronous human review state.
type Human struct {
	Status string `yaml:"status"` // pending | accepted | rejected
	By     string `yaml:"by,omitempty"`
	At     string `yaml:"at,omitempty"`
	Note   string `yaml:"note,omitempty"`
}

// Ledger is every capability's lock, keyed by capability directory name.
type Ledger map[string]*LockFile

func lockPath(root, capability string) string {
	return filepath.Join(root, "specs", capability, "spec.lock.yaml")
}

// ReadLedger loads every specs/*/spec.lock.yaml.
func ReadLedger(root string) (Ledger, error) {
	l := Ledger{}
	matches, err := filepath.Glob(filepath.Join(root, "specs", "*", "spec.lock.yaml"))
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		lf := &LockFile{}
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(b, lf); err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		if lf.Scenarios == nil {
			lf.Scenarios = map[string]*LockEntry{}
		}
		l[filepath.Base(filepath.Dir(m))] = lf
	}
	return l, nil
}

// Entry finds a scenario's lock entry in any capability.
func (l Ledger) Entry(id string) (*LockEntry, string) {
	for capability, lf := range l {
		if e, ok := lf.Scenarios[id]; ok {
			return e, capability
		}
	}
	return nil, ""
}

func (l Ledger) set(capability, id string, e *LockEntry) {
	lf, ok := l[capability]
	if !ok {
		lf = &LockFile{Version: 1, Scenarios: map[string]*LockEntry{}}
		l[capability] = lf
	}
	lf.Scenarios[id] = e
}

// Write saves every capability's lock.
func (l Ledger) Write(root string) error {
	header := "# Approval ledger for this capability. Edit only with `go tool gate spec approve|human`.\n# See docs/engineering/spec-review.md.\n"
	for capability, lf := range l {
		lf.Version = 1
		b, err := yaml.Marshal(lf)
		if err != nil {
			return err
		}
		if err := os.WriteFile(lockPath(root, capability), append([]byte(header), b...), 0o644); err != nil {
			return err
		}
	}
	return nil
}

var tierRank = map[string]int{"P0": 0, "P1": 1, "P2": 2, "P3": 3}

// MinTier is the rule-based minimum tier (the most critical tier the rules demand).
func MinTier(sc *Scenario, prev *LockEntry) string {
	switch {
	case sc.Critical():
		return "P0"
	case prev != nil && prev.Hash != sc.Hash:
		return "P1" // changes agreed behavior
	default:
		return "P3"
	}
}

// CheckLock verifies every scenario is approved at its current text.
func CheckLock(s *Spec, l Ledger) []string {
	var errs []string
	present := map[string]bool{}
	for _, sc := range s.Scenarios {
		if sc.ID == "" {
			continue
		}
		present[sc.ID] = true
		at := fmt.Sprintf("%s (%s:%d)", sc.ID, sc.File, sc.Line)
		e, capability := l.Entry(sc.ID)
		if e == nil {
			errs = append(errs, at+": not approved; run the spec review (docs/engineering/spec-review.md)")
			continue
		}
		if capability != sc.Capability {
			errs = append(errs, fmt.Sprintf("%s: approval is in specs/%s/spec.lock.yaml, expected specs/%s/", at, capability, sc.Capability))
		}
		if e.Hash != sc.Hash {
			errs = append(errs, at+": text changed since approval; a spec-change review is required")
		}
		if strings.Join(e.Reqs, ",") != strings.Join(sc.Reqs, ",") {
			errs = append(errs, at+": requirement tags changed since approval")
		}
		if _, ok := tierRank[e.Tier]; !ok {
			errs = append(errs, at+": invalid tier "+e.Tier)
		} else if sc.Critical() && e.Tier != "P0" {
			errs = append(errs, at+": @security/@cost/@contract scenarios must be tier P0")
		}
		if (e.Tier == "P0" || e.Tier == "P1") && strings.TrimSpace(e.Note) == "" {
			errs = append(errs, at+": P0 and P1 approvals need a note explaining the tier")
		}
		if err := checkApprovers(e.Author, e.Reviewer); err != nil {
			errs = append(errs, at+": "+err.Error())
		}
		switch e.Human.Status {
		case "pending":
		case "accepted", "rejected":
			if by, err := ParseIdentity(e.Human.By); err != nil || !by.Human() {
				errs = append(errs, at+": human."+e.Human.Status+" needs `by: human:<login>`; record it with `gate spec human`")
			}
			if e.Human.Status == "rejected" {
				errs = append(errs, at+": rejected by a human reviewer; revise via spec-change")
			}
		default:
			errs = append(errs, at+": human.status must be pending|accepted|rejected")
		}
	}
	for capability, lf := range l {
		for id := range lf.Scenarios {
			if !present[id] {
				errs = append(errs, fmt.Sprintf("%s (specs/%s/spec.lock.yaml): lock entry without scenario; remove it in the change that deleted the scenario", id, capability))
			}
		}
	}
	sort.Strings(errs)
	return errs
}

func checkApprovers(author, reviewer string) error {
	a, err := ParseIdentity(author)
	if err != nil {
		return fmt.Errorf("author: %w", err)
	}
	r, err := ParseIdentity(reviewer)
	if err != nil {
		return fmt.Errorf("reviewer: %w", err)
	}
	if !a.Human() && a.Role != RoleSpecAuthor {
		return fmt.Errorf("author %s must have role %s", author, RoleSpecAuthor)
	}
	if !r.Human() && r.Role != RoleSpecReviewer {
		return fmt.Errorf("reviewer %s must have role %s", reviewer, RoleSpecReviewer)
	}
	if a.Instance == r.Instance {
		return errors.New("author and reviewer are the same agent instance or person")
	}
	return nil
}

// ApproveRequest is one approval decision.
type ApproveRequest struct {
	IDs      []string // scenario ids; "*unapproved" selects every unapproved or changed scenario
	Tier     string
	Author   string
	Reviewer string
	Note     string
	Ref      string
	Date     string
}

// Approve records approval for scenarios at their current text.
//
// Approving text that is already approved cannot undo what was decided about
// it: a person's acceptance or rejection stays, and the tier does not become
// less critical. Only new text starts again at "pending".
func Approve(s *Spec, l Ledger, r ApproveRequest) ([]string, error) {
	if _, ok := tierRank[r.Tier]; !ok {
		return nil, fmt.Errorf("tier must be P0..P3")
	}
	if err := checkApprovers(r.Author, r.Reviewer); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range r.IDs {
		want[id] = true
	}
	var done []string
	for _, sc := range s.Scenarios {
		prev, _ := l.Entry(sc.ID)
		unapproved := prev == nil || prev.Hash != sc.Hash
		if !want[sc.ID] && (!want["*unapproved"] || !unapproved) {
			continue
		}
		t := r.Tier
		if minT := MinTier(sc, prev); tierRank[minT] < tierRank[t] {
			t = minT
		}
		human := Human{Status: "pending"}
		if !unapproved {
			human = prev.Human
			if rank, ok := tierRank[prev.Tier]; ok && rank < tierRank[t] {
				t = prev.Tier
			}
		}
		if (t == "P0" || t == "P1") && strings.TrimSpace(r.Note) == "" {
			return nil, fmt.Errorf("%s is tier %s: -note is required", sc.ID, t)
		}
		l.set(sc.Capability, sc.ID, &LockEntry{Hash: sc.Hash, Reqs: sc.Reqs, Tier: t, Author: r.Author, Reviewer: r.Reviewer,
			ApprovedAt: r.Date, Note: r.Note, Ref: r.Ref, Human: human})
		done = append(done, sc.ID+"="+t)
	}
	return done, nil
}

// Queue lists scenarios awaiting human review, most critical first.
func Queue(l Ledger) []string {
	type row struct{ id, tier, note string }
	var rows []row
	for _, lf := range l {
		for id, e := range lf.Scenarios {
			if e.Human.Status == "pending" {
				rows = append(rows, row{id, e.Tier, e.Note})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].tier != rows[j].tier {
			return tierRank[rows[i].tier] < tierRank[rows[j].tier]
		}
		return rows[i].id < rows[j].id
	})
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s  %-12s %s", r.tier, r.id, r.note))
	}
	return out
}

// ReleaseCheck fails while any P0 scenario lacks human acceptance.
func ReleaseCheck(l Ledger) []string {
	var errs []string
	for _, lf := range l {
		for id, e := range lf.Scenarios {
			if e.Tier == "P0" && e.Human.Status != "accepted" {
				errs = append(errs, fmt.Sprintf("%s: P0 scenario not yet accepted by a person (%s)", id, e.Human.Status))
			}
		}
	}
	sort.Strings(errs)
	return errs
}
