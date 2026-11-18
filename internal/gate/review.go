package gate

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Review risk tiers, most demanding first. The tier of a change is the highest
// tier of any path it touches; it sets review depth (docs/engineering/code-review.md).
var riskTiers = []string{"high", "medium", "low"}

// ReviewRules is review.yaml: path globs per risk tier.
type ReviewRules struct {
	Default string              `yaml:"default"`
	Tiers   map[string][]string `yaml:"tiers"`
	// HighOwner lists the reviewer and the architect; the host requires the
	// approval of one of them on high-tier paths
	// (docs/engineering/agent-workflow.md §7.1).
	HighOwner string `yaml:"high_owner"`
}

// CodeOwners renders .github/CODEOWNERS from the high tier, so the host
// enforces the same paths the review plan reports.
func (r ReviewRules) CodeOwners() string {
	var b strings.Builder
	b.WriteString("# Generated from review.yaml by `go tool gate docs -write`. Do not edit.\n")
	b.WriteString("# High-tier paths need the approval of the reviewer or the architect.\n")
	for _, g := range r.Tiers["high"] {
		if rest, ok := strings.CutPrefix(g, "**/"); ok {
			g = rest // a bare name matches at any depth
		} else {
			g = "/" + g
		}
		fmt.Fprintf(&b, "%s %s\n", g, r.HighOwner)
	}
	return b.String()
}

// ReviewPlan tells reviewers how deep to go and what to read.
type ReviewPlan struct {
	Tier       string            `json:"tier"`
	Files      map[string]string `json:"files"`       // changed file -> tier
	ReviewDocs []string          `json:"review_docs"` // REVIEW.md files that apply, from the base
}

// globRegexp turns a glob with ** (any depth) into an anchored regexp.
func globRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			b.WriteString(".*")
			i++
			if i+1 < len(glob) && glob[i+1] == '/' {
				i++ // "**/" also matches zero directories
				b.WriteString("/?")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// TierOf returns the risk tier of one repository path.
func (r ReviewRules) TierOf(file string) string {
	for _, t := range riskTiers {
		for _, g := range r.Tiers[t] {
			if globRegexp(g).MatchString(file) {
				return t
			}
		}
	}
	if r.Default != "" {
		return r.Default
	}
	return "medium"
}

func rank(tier string) int {
	for i, t := range riskTiers {
		if t == tier {
			return i
		}
	}
	return len(riskTiers)
}

// checkRefNotOption rejects a ref git would read as an option before it
// reaches any git command. name says which flag it came from, so the error
// names what to fix.
func checkRefNotOption(name, ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("%s %q begins with '-': pass a branch, tag or commit, not a git option", name, ref)
	}
	return nil
}

// checkBaseRef rejects a value git would read as an option and requires the
// ref to name exactly one commit. Branches, tags, remote-tracking refs such as
// origin/main and commit SHAs all pass: the gate compares against a commit,
// not against a branch name, so a session branch's shape is not required.
func checkBaseRef(root, base string) error {
	if base == "" {
		return errors.New("no base ref: pass a branch, tag or commit such as origin/main")
	}
	if err := checkRefNotOption("base ref", base); err != nil {
		return err
	}
	out, err := git(root, "rev-parse", "--verify", "--quiet", "--end-of-options", base+"^{commit}")
	// A revision-set negation such as ^main also exits 0, printing ^<sha>.
	// Only one full object name is a commit; anything else would make `git
	// show` and `git diff` read an empty range.
	if err != nil || !isObjectID(out) {
		return fmt.Errorf("base ref %q does not name a commit in this clone: fetch it (`git fetch origin`) or pass a branch, tag or commit SHA", base)
	}
	return nil
}

// isObjectID reports whether s is one full object name: rev-parse prints one
// line of hex for a commit, and ^<sha> for a revision-set negation.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 { // SHA-1 or SHA-256
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// PlanReview reads review.yaml and the REVIEW.md files from the base ref, so a
// change cannot lower the bar it is reviewed against.
func PlanReview(root, base string, changed []string) (ReviewPlan, error) {
	if err := checkBaseRef(root, base); err != nil {
		return ReviewPlan{}, err
	}
	raw, err := git(root, "show", base+":review.yaml")
	if err != nil {
		b, ferr := os.ReadFile(filepath.Join(root, "review.yaml")) // first introduction
		if ferr != nil {
			return ReviewPlan{}, fmt.Errorf("review.yaml: %w", err)
		}
		raw = string(b)
	}
	var rules ReviewRules
	if err := yaml.Unmarshal([]byte(raw), &rules); err != nil {
		return ReviewPlan{}, fmt.Errorf("review.yaml: %w", err)
	}
	plan := ReviewPlan{Tier: riskTiers[len(riskTiers)-1], Files: map[string]string{}}
	docs := map[string]bool{}
	for _, f := range changed {
		t := rules.TierOf(f)
		plan.Files[f] = t
		if rank(t) < rank(plan.Tier) {
			plan.Tier = t
		}
		for dir := path.Dir(f); ; dir = path.Dir(dir) {
			doc := path.Join(dir, "REVIEW.md")
			if dir == "." {
				doc = "REVIEW.md"
			}
			if _, err := git(root, "cat-file", "-e", base+":"+doc); err == nil {
				docs[doc] = true
			}
			if dir == "." || dir == "/" {
				break
			}
		}
	}
	plan.ReviewDocs = sortedKeys(docs)
	sort.Strings(plan.ReviewDocs)
	return plan, nil
}

// ChangedFiles lists the files a change touches: everything that differs
// between the point where head left base and head itself. An empty head means
// the working tree, so uncommitted edits count. Comparing from the merge base
// keeps commits that landed on base in the meantime out of the change.
func ChangedFiles(root, base, head string) ([]string, error) {
	if err := checkBaseRef(root, base); err != nil {
		return nil, err
	}
	if head != "" {
		if err := checkRefNotOption("head", head); err != nil {
			return nil, err
		}
	}
	tip := head
	if tip == "" {
		tip = "HEAD"
	}
	from, err := git(root, "merge-base", base, tip)
	if err != nil {
		return nil, err
	}
	args := []string{"diff", "--name-only", from}
	if head != "" {
		args = append(args, head)
	}
	out, err := git(root, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}
