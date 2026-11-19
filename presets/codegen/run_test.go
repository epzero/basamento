package codegen

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type fakeModel struct {
	plan      Plan
	repairs   []Workspace
	reviews   [][]Finding
	requests  []Request
	repairIdx int
	reviewIdx int
}

func (m *fakeModel) Complete(_ context.Context, req Request) (json.RawMessage, error) {
	m.requests = append(m.requests, req)
	switch req.Agent {
	case "planner":
		return json.Marshal(m.plan)
	case "implement_module":
		return json.Marshal(Workspace{ID: "ws-" + req.Module})
	case "repair":
		if m.repairIdx < len(m.repairs) {
			ws := m.repairs[m.repairIdx]
			m.repairIdx++
			return json.Marshal(ws)
		}
		return json.Marshal(Workspace{ID: "ws-repair"})
	case "reviewer":
		var findings []Finding
		if m.reviewIdx < len(m.reviews) {
			findings = m.reviews[m.reviewIdx]
			m.reviewIdx++
		}
		return json.Marshal(Findings{Findings: findings})
	}
	return nil, fmt.Errorf("unexpected agent %q", req.Agent)
}

func (m *fakeModel) count(agent string) int {
	n := 0
	for _, req := range m.requests {
		if req.Agent == agent {
			n++
		}
	}
	return n
}

type fakeSandbox struct {
	results []TestResult
	calls   []Workspace
}

func (s *fakeSandbox) RunTests(_ context.Context, ws Workspace, _ string) (TestResult, error) {
	s.calls = append(s.calls, ws)
	if len(s.results) == 0 {
		return TestResult{}, nil
	}
	next := s.results[0]
	if len(s.results) > 1 {
		s.results = s.results[1:]
	}
	return next, nil
}

type fakeGit struct {
	commits []Commit
}

func (g *fakeGit) Commit(_ context.Context, c Commit) error {
	g.commits = append(g.commits, c)
	return nil
}

func bootstrapRunner(model Model, sandbox Sandbox, git GitHost, guards Guards) *Runner {
	return New(Config{
		Flow: FlowBootstrap, Version: "v1", Prompts: "v1/prompts", Evals: "v1/evals",
		Guards: guards, RepairVisitCost: 1000,
	}, model, sandbox, git)
}

func TestBootstrapCommitsTheLastVerifiedWorkspace(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: Plan{Modules: []Module{{Name: "store"}, {Name: "api"}}}}
	sandbox := &fakeSandbox{}
	git := &fakeGit{}
	st, err := bootstrapRunner(model, sandbox, git, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := st.StageLog, []Stage{StagePlan, StageImplement, StageVerify, StageSelfReview, StageCommit}; !equalStages(got, want) {
		t.Fatalf("stage log %v, want %v", got, want)
	}
	for _, stage := range []Stage{StagePlan, StageImplement, StageVerify, StageSelfReview, StageCommit} {
		if !st.Checkpoints[stage] {
			t.Fatalf("stage %q has no checkpoint", stage)
		}
	}
	if st.Checkpoints[StageRepair] {
		t.Fatal("repair was checkpointed in a passing run")
	}
	if len(git.commits) != 1 || !git.commits[0].NewRepo {
		t.Fatalf("commits %+v, want one first commit", git.commits)
	}
	if git.commits[0].Workspace.ID != "ws-api" {
		t.Fatalf("committed %q, want the last verified workspace ws-api", git.commits[0].Workspace.ID)
	}
}

func TestRepairLoopsUnderTheGuard(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: Plan{Modules: []Module{{Name: "store"}}}}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"a", "b"}}, {Failures: []string{"a"}}, {}}}
	git := &fakeGit{}
	st, err := bootstrapRunner(model, sandbox, git, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Stage{StagePlan, StageImplement, StageVerify, StageRepair, StageVerify, StageRepair, StageVerify, StageSelfReview, StageCommit}
	if !equalStages(st.StageLog, want) {
		t.Fatalf("stage log %v, want %v", st.StageLog, want)
	}
	if st.RepairVisits != 2 {
		t.Fatalf("repair visits %d, want 2", st.RepairVisits)
	}
}

func TestRepairVisitLimitFails(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: Plan{Modules: []Module{{Name: "store"}}}}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"tests fail"}}}}
	st, err := bootstrapRunner(model, sandbox, &fakeGit{}, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Failed || st.Limit != "visits" || st.Code != "budget_exceeded" {
		t.Fatalf("state %+v, want failed budget_exceeded visits", st)
	}
	if st.RepairVisits != 3 {
		t.Fatalf("repair visits %d, want 3", st.RepairVisits)
	}
	if len(st.Failures) != 1 || st.Failures[0] != "tests fail" {
		t.Fatalf("failures %v, want the last test failures", st.Failures)
	}
}

func TestRepairCostLimitFailsBeforeVisits(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: Plan{Modules: []Module{{Name: "store"}}}}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"tests fail"}}}}
	r := bootstrapRunner(model, sandbox, &fakeGit{}, DefaultGuards())
	r.Config.RepairVisitCost = 2500
	st, err := r.Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Failed || st.Limit != "cost" || st.RepairVisits != 2 {
		t.Fatalf("state %+v, want failed cost after 2 repairs", st)
	}
}

func TestIterateCommitsOnANewBranch(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: Plan{Modules: []Module{{Name: "parser"}}}}
	git := &fakeGit{}
	r := New(Config{Flow: FlowIterate, Version: "v1", Prompts: "v1/prompts", Evals: "v1/evals", Guards: DefaultGuards()}, model, &fakeSandbox{}, git)
	st, err := r.Start(context.Background(), Input{Repo: "acme/app", BaseBranch: "release/v1"}, NoControl{})
	if err != nil {
		t.Fatal(err)
	}
	if len(git.commits) != 1 {
		t.Fatalf("commits %+v, want one", git.commits)
	}
	want := "codegen/" + st.RunID
	if git.commits[0].Branch != want || git.commits[0].From != "release/v1" {
		t.Fatalf("commit %+v, want branch %s from release/v1", git.commits[0], want)
	}
}

type stopAfter struct {
	stage Stage
}

func (s stopAfter) BeforeStage(context.Context, Stage) error { return nil }
func (s stopAfter) AfterStage(_ context.Context, stage Stage) error {
	if stage == s.stage {
		return ErrStop
	}
	return nil
}

func TestResumeDoesNotRepeatACompletedStage(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: Plan{Modules: []Module{{Name: "store"}, {Name: "api"}}}}
	sandbox := &fakeSandbox{}
	git := &fakeGit{}
	r := bootstrapRunner(model, sandbox, git, DefaultGuards())
	st, err := r.Start(context.Background(), Input{Brief: "invoices"}, stopAfter{StageImplement})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Stopped {
		t.Fatal("run did not stop after implement")
	}
	if _, err := r.Resume(context.Background(), st, NoControl{}); err != nil {
		t.Fatal(err)
	}
	if got := model.count("planner"); got != 1 {
		t.Fatalf("planner ran %d times, want 1", got)
	}
	if got := model.count("implement_module"); got != 2 {
		t.Fatalf("implement ran %d times, want 2", got)
	}
	if len(sandbox.calls) != 1 {
		t.Fatalf("sandbox ran %d times, want 1", len(sandbox.calls))
	}
	if len(git.commits) != 1 {
		t.Fatalf("commits %d, want 1", len(git.commits))
	}
}

func TestResumeKeepsThePinnedVersion(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: Plan{Modules: []Module{{Name: "store"}}}}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"a"}}, {}}}
	r := bootstrapRunner(model, sandbox, &fakeGit{}, DefaultGuards())
	st, err := r.Start(context.Background(), Input{Brief: "invoices"}, stopAfter{StageVerify})
	if err != nil {
		t.Fatal(err)
	}
	// A new version becoming current does not change the run's pin.
	if _, err := r.Resume(context.Background(), st, NoControl{}); err != nil {
		t.Fatal(err)
	}
	if st.Version != "v1" || st.Prompts != "v1/prompts" {
		t.Fatalf("run version %q prompts %q, want v1", st.Version, st.Prompts)
	}
	for _, req := range model.requests {
		if req.Agent == "repair" && req.PromptVersion != "v1/prompts" {
			t.Fatalf("repair used prompt version %q, want v1/prompts", req.PromptVersion)
		}
	}
}

func equalStages(a, b []Stage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
