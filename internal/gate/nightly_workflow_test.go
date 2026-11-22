package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// nightlyWorkflowJob is one GitHub Actions job in nightly.yml.
type nightlyWorkflowJob struct {
	Env   map[string]string     `yaml:"env"`
	Steps []nightlyWorkflowStep `yaml:"steps"`
}

// nightlyWorkflowStep is one step in a nightly job.
type nightlyWorkflowStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

// nightlyWorkflowDoc is the subset of nightly.yml this package checks.
type nightlyWorkflowDoc struct {
	Jobs map[string]nightlyWorkflowJob `yaml:"jobs"`
}

// nightlyJobExpect pins the go test invocation for a Postgres-guarded job.
type nightlyJobExpect struct {
	jobName      string
	testTimeout  string
	testCount    string // empty means not pinned (slow-lanes)
	testRunMatch func(run string) bool
}

// parseNightlyWorkflow decodes nightly.yml into the fields the Postgres guards use.
func parseNightlyWorkflow(data []byte) (*nightlyWorkflowDoc, error) {
	var doc nightlyWorkflowDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// cloneNightlyDoc copies a parsed workflow so mutation tests cannot touch the file.
func cloneNightlyDoc(doc *nightlyWorkflowDoc) *nightlyWorkflowDoc {
	out := &nightlyWorkflowDoc{Jobs: make(map[string]nightlyWorkflowJob, len(doc.Jobs))}
	for name, job := range doc.Jobs {
		out.Jobs[name] = cloneNightlyJob(job)
	}
	return out
}

func cloneNightlyJob(job nightlyWorkflowJob) nightlyWorkflowJob {
	env := make(map[string]string, len(job.Env))
	for k, v := range job.Env {
		env[k] = v
	}
	steps := make([]nightlyWorkflowStep, len(job.Steps))
	for i, s := range job.Steps {
		steps[i] = cloneNightlyStep(s)
	}
	return nightlyWorkflowJob{Env: env, Steps: steps}
}

func cloneNightlyStep(s nightlyWorkflowStep) nightlyWorkflowStep {
	env := make(map[string]string, len(s.Env))
	for k, v := range s.Env {
		env[k] = v
	}
	with := make(map[string]any, len(s.With))
	for k, v := range s.With {
		with[k] = v
	}
	return nightlyWorkflowStep{Name: s.Name, Run: s.Run, Uses: s.Uses, Env: env, With: with}
}

// checkNightlyPostgresJob lists every missing Postgres guard for one nightly job.
func checkNightlyPostgresJob(doc *nightlyWorkflowDoc, expect nightlyJobExpect) []string {
	job, ok := doc.Jobs[expect.jobName]
	if !ok {
		return []string{fmt.Sprintf("%s: job not found", expect.jobName)}
	}
	var missing []string
	missing = append(missing, checkNightlyStrict(expect.jobName, job)...)
	prefetchIdx, cacheIdx, testIdx := locateNightlySteps(job, expect)
	missing = append(missing, checkNightlyCache(expect.jobName, job, cacheIdx)...)
	missing = append(missing, checkNightlyPrefetchOrder(expect.jobName, prefetchIdx, testIdx)...)
	missing = append(missing, checkNightlyGoTest(expect.jobName, job, testIdx, expect)...)
	return missing
}

func checkNightlyStrict(jobName string, job nightlyWorkflowJob) []string {
	if strict, ok := job.Env["GATE_STRICT"]; ok && strict == "1" {
		return nil
	}
	return []string{fmt.Sprintf("%s: GATE_STRICT must be \"1\" at job level", jobName)}
}

func locateNightlySteps(job nightlyWorkflowJob, expect nightlyJobExpect) (prefetch, cache, test int) {
	prefetch, cache, test = -1, -1, -1
	for i, step := range job.Steps {
		if strings.Contains(step.Run, "make pg-prefetch") {
			prefetch = i
		}
		if strings.HasPrefix(step.Uses, "actions/cache@") {
			cache = i
		}
		if step.Run != "" && strings.Contains(step.Run, "go test") && expect.testRunMatch(step.Run) {
			test = i
		}
	}
	return prefetch, cache, test
}

func checkNightlyCache(jobName string, job nightlyWorkflowJob, cacheIdx int) []string {
	if cacheIdx < 0 {
		return []string{fmt.Sprintf("%s: missing actions/cache Postgres bundle cache step", jobName)}
	}
	var missing []string
	key, _ := job.Steps[cacheIdx].With["key"].(string)
	if !strings.Contains(key, "PG_MODULE_VERSION") {
		missing = append(missing, fmt.Sprintf("%s: cache key must use PG_MODULE_VERSION from go.mod", jobName))
	}
	if regexp.MustCompile(`embedded-postgres-v[0-9]`).MatchString(key) {
		missing = append(missing, fmt.Sprintf("%s: cache key must not use a hand-written module version", jobName))
	}
	if !nightlyHasModuleVersionStep(job.Steps[:cacheIdx+1]) {
		missing = append(missing, fmt.Sprintf("%s: missing Postgres bundle cache location and version step", jobName))
	}
	return missing
}

func nightlyHasModuleVersionStep(steps []nightlyWorkflowStep) bool {
	for _, step := range steps {
		if strings.Contains(step.Run, "go list -m") && strings.Contains(step.Run, "embedded-postgres") {
			return true
		}
	}
	return false
}

func checkNightlyPrefetchOrder(jobName string, prefetchIdx, testIdx int) []string {
	if prefetchIdx < 0 {
		return []string{fmt.Sprintf("%s: missing step that runs make pg-prefetch", jobName)}
	}
	if testIdx < 0 {
		return nil
	}
	if prefetchIdx >= testIdx {
		return []string{fmt.Sprintf("%s: make pg-prefetch must run before go test", jobName)}
	}
	return nil
}

func checkNightlyGoTest(jobName string, job nightlyWorkflowJob, testIdx int, expect nightlyJobExpect) []string {
	if testIdx < 0 {
		return []string{fmt.Sprintf("%s: missing go test step", jobName)}
	}
	run := job.Steps[testIdx].Run
	var missing []string
	if !strings.Contains(run, "-timeout "+expect.testTimeout) {
		missing = append(missing, fmt.Sprintf("%s: go test must pass -timeout %s", jobName, expect.testTimeout))
	}
	if expect.testCount != "" {
		countPat := `(^|\s)-count=` + regexp.QuoteMeta(expect.testCount) + `(\s|$)`
		if !regexp.MustCompile(countPat).MatchString(run) {
			missing = append(missing, fmt.Sprintf("%s: go test must pass -count=%s", jobName, expect.testCount))
		}
	}
	return missing
}

func nightlyJobExpects() map[string]nightlyJobExpect {
	return map[string]nightlyJobExpect{
		"flake-hunt": {
			jobName:     "flake-hunt",
			testTimeout: "90m",
			testCount:   "10",
			testRunMatch: func(run string) bool {
				return strings.Contains(run, "./...")
			},
		},
		"slow-lanes": {
			jobName:     "slow-lanes",
			testTimeout: "30m",
			testRunMatch: func(run string) bool {
				return strings.Contains(run, "./internal/bdd/")
			},
		},
	}
}

// TestNightlyWorkflowPostgresGuards pins flake-hunt and slow-lanes on strict,
// prefetched Postgres, flake-hunt's -count=10 and per-job -timeout (EP0-154).
func TestNightlyWorkflowPostgresGuards(t *testing.T) {
	doc := readNightlyWorkflow(t)
	for name, exp := range nightlyJobExpects() {
		t.Run(name, func(t *testing.T) {
			if missing := checkNightlyPostgresJob(doc, exp); len(missing) > 0 {
				t.Fatalf("nightly Postgres guards:\n%s", strings.Join(missing, "\n"))
			}
		})
	}
}

func readNightlyWorkflow(t *testing.T) *nightlyWorkflowDoc {
	t.Helper()
	path := filepath.Join(makefileRoot(t), ".github", "workflows", "nightly.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseNightlyWorkflow(data)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestNightlyWorkflowPostgresGuardsMutations proves each guard fails when removed.
func TestNightlyWorkflowPostgresGuardsMutations(t *testing.T) {
	base := readNightlyWorkflow(t)
	expects := nightlyJobExpects()
	cases := []struct {
		job    string
		name   string
		mutate func(*nightlyWorkflowDoc)
		want   string
	}{
		{"flake-hunt", "no GATE_STRICT", func(d *nightlyWorkflowDoc) {
			j := d.Jobs["flake-hunt"]
			delete(j.Env, "GATE_STRICT")
			d.Jobs["flake-hunt"] = j
		}, "GATE_STRICT"},
		{"flake-hunt", "no pg-prefetch", func(d *nightlyWorkflowDoc) {
			j := dropNightlySteps(d.Jobs["flake-hunt"], func(s nightlyWorkflowStep) bool {
				return strings.Contains(s.Run, "make pg-prefetch")
			})
			d.Jobs["flake-hunt"] = j
		}, "make pg-prefetch"},
		{"flake-hunt", "no cache", func(d *nightlyWorkflowDoc) {
			j := dropNightlySteps(d.Jobs["flake-hunt"], func(s nightlyWorkflowStep) bool {
				return strings.HasPrefix(s.Uses, "actions/cache@")
			})
			d.Jobs["flake-hunt"] = j
		}, "actions/cache"},
		{"flake-hunt", "prefetch after test", func(d *nightlyWorkflowDoc) {
			d.Jobs["flake-hunt"] = reorderPrefetchAfterTest(d.Jobs["flake-hunt"])
		}, "make pg-prefetch must run before go test"},
		{"flake-hunt", "no test timeout", func(d *nightlyWorkflowDoc) {
			d.Jobs["flake-hunt"] = stripNightlyTestTimeout(d.Jobs["flake-hunt"], expects["flake-hunt"])
		}, "-timeout 90m"},
		{"flake-hunt", "count lowered to 1", func(d *nightlyWorkflowDoc) {
			d.Jobs["flake-hunt"] = replaceNightlyTestCount(d.Jobs["flake-hunt"], expects["flake-hunt"], "10", "1")
		}, "-count=10"},
		{"flake-hunt", "no count flag", func(d *nightlyWorkflowDoc) {
			d.Jobs["flake-hunt"] = stripNightlyTestCount(d.Jobs["flake-hunt"], expects["flake-hunt"])
		}, "-count=10"},
		{"slow-lanes", "no GATE_STRICT", func(d *nightlyWorkflowDoc) {
			j := d.Jobs["slow-lanes"]
			delete(j.Env, "GATE_STRICT")
			d.Jobs["slow-lanes"] = j
		}, "GATE_STRICT"},
		{"slow-lanes", "no pg-prefetch", func(d *nightlyWorkflowDoc) {
			j := dropNightlySteps(d.Jobs["slow-lanes"], func(s nightlyWorkflowStep) bool {
				return strings.Contains(s.Run, "make pg-prefetch")
			})
			d.Jobs["slow-lanes"] = j
		}, "make pg-prefetch"},
		{"slow-lanes", "no cache", func(d *nightlyWorkflowDoc) {
			j := dropNightlySteps(d.Jobs["slow-lanes"], func(s nightlyWorkflowStep) bool {
				return strings.HasPrefix(s.Uses, "actions/cache@")
			})
			d.Jobs["slow-lanes"] = j
		}, "actions/cache"},
		{"slow-lanes", "no test timeout", func(d *nightlyWorkflowDoc) {
			d.Jobs["slow-lanes"] = stripNightlyTestTimeout(d.Jobs["slow-lanes"], expects["slow-lanes"])
		}, "-timeout 30m"},
	}
	for _, tc := range cases {
		t.Run(tc.job+"/"+tc.name, func(t *testing.T) {
			doc := cloneNightlyDoc(base)
			tc.mutate(doc)
			missing := checkNightlyPostgresJob(doc, expects[tc.job])
			if len(missing) == 0 {
				t.Fatalf("mutation %q: want a missing guard", tc.name)
			}
			joined := strings.Join(missing, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("mutation %q: missing messages do not contain %q:\n%s", tc.name, tc.want, joined)
			}
		})
	}
}

func dropNightlySteps(job nightlyWorkflowJob, drop func(nightlyWorkflowStep) bool) nightlyWorkflowJob {
	out := cloneNightlyJob(job)
	var kept []nightlyWorkflowStep
	for _, s := range out.Steps {
		if !drop(s) {
			kept = append(kept, s)
		}
	}
	out.Steps = kept
	return out
}

func reorderPrefetchAfterTest(job nightlyWorkflowJob) nightlyWorkflowJob {
	out := cloneNightlyJob(job)
	var prefetch, rest []nightlyWorkflowStep
	for _, s := range out.Steps {
		if strings.Contains(s.Run, "make pg-prefetch") {
			prefetch = append(prefetch, s)
			continue
		}
		rest = append(rest, s)
	}
	steps := append([]nightlyWorkflowStep(nil), rest...)
	steps = append(steps, prefetch...)
	out.Steps = steps
	return out
}

func stripNightlyTestTimeout(job nightlyWorkflowJob, expect nightlyJobExpect) nightlyWorkflowJob {
	out := cloneNightlyJob(job)
	for i, s := range out.Steps {
		if s.Run == "" || !strings.Contains(s.Run, "go test") || !expect.testRunMatch(s.Run) {
			continue
		}
		s.Run = strings.ReplaceAll(s.Run, "-timeout "+expect.testTimeout, "")
		out.Steps[i] = s
	}
	return out
}

func stripNightlyTestCount(job nightlyWorkflowJob, expect nightlyJobExpect) nightlyWorkflowJob {
	out := cloneNightlyJob(job)
	if expect.testCount == "" {
		return out
	}
	countRe := regexp.MustCompile(`(^|\s)-count=` + regexp.QuoteMeta(expect.testCount) + `(\s|$)`)
	for i, s := range out.Steps {
		if s.Run == "" || !strings.Contains(s.Run, "go test") || !expect.testRunMatch(s.Run) {
			continue
		}
		s.Run = strings.TrimSpace(countRe.ReplaceAllString(s.Run, " "))
		out.Steps[i] = s
	}
	return out
}

func replaceNightlyTestCount(job nightlyWorkflowJob, expect nightlyJobExpect, from, to string) nightlyWorkflowJob {
	out := cloneNightlyJob(job)
	for i, s := range out.Steps {
		if s.Run == "" || !strings.Contains(s.Run, "go test") || !expect.testRunMatch(s.Run) {
			continue
		}
		s.Run = strings.ReplaceAll(s.Run, "-count="+from, "-count="+to)
		out.Steps[i] = s
	}
	return out
}
