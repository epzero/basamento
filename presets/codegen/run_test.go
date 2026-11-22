package codegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/epzero/basamento/core"
)

type fakeModel struct {
	plan      Plan
	moduleWS  map[string]Workspace
	repairs   []Workspace
	reviews   [][]Finding
	raw       map[string]json.RawMessage
	requests  []Request
	repairIdx int
	reviewIdx int
}

func (m *fakeModel) Complete(_ context.Context, req Request) (json.RawMessage, error) {
	m.requests = append(m.requests, req)
	if raw, ok := m.raw[req.Agent]; ok {
		return raw, nil
	}
	switch req.Agent {
	case "planner":
		return json.Marshal(m.plan)
	case "implement_module":
		if ws, ok := m.moduleWS[req.Module]; ok {
			return json.Marshal(ws)
		}
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
		return json.Marshal(Findings{Findings: append([]Finding{}, findings...)})
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

func (m *fakeModel) request(agent string, n int) *Request {
	seen := 0
	for i := range m.requests {
		if m.requests[i].Agent == agent {
			if seen == n {
				return &m.requests[i]
			}
			seen++
		}
	}
	return nil
}

type fakeSandbox struct {
	results  []TestResult
	calls    []Workspace
	commands []string
}

func (s *fakeSandbox) RunTests(_ context.Context, ws Workspace, command string) (TestResult, error) {
	s.calls = append(s.calls, ws)
	s.commands = append(s.commands, command)
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

func planOf(modules ...string) Plan {
	plan := Plan{Tests: []string{"go test ./..."}}
	for _, name := range modules {
		plan.Modules = append(plan.Modules, Module{Name: name})
	}
	return plan
}

func fixedID(id string) func() (string, error) {
	return func() (string, error) { return id, nil }
}

func bootstrapRunner(model Model, sandbox Sandbox, git GitHost, guards Guards) *Runner {
	r := New(Config{
		Flow: FlowBootstrap, Version: "v1", Prompts: "v1/prompts", Evals: "v1/evals",
		Guards: guards, RepairVisitCost: 1000,
	}, model, sandbox, git)
	r.IDs = fixedID("run-test")
	return r
}

func TestBootstrapCommitsTheLastVerifiedWorkspace(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("store", "api")}
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
	model := &fakeModel{plan: planOf("store")}
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
	model := &fakeModel{plan: planOf("store")}
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
	model := &fakeModel{plan: planOf("store")}
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

// S5c: the repair visit cost is pinned at Start, so a resume rebuilt with a
// cheaper config still stops at the guard the run started under.
func TestResumePinsTheRepairVisitCost(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("store")}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"tests fail"}}}}
	r := bootstrapRunner(model, sandbox, &fakeGit{}, DefaultGuards())
	r.Config.RepairVisitCost = 2500
	st, err := r.Start(context.Background(), Input{Brief: "invoices"}, stopAfter{StageVerify})
	if err != nil {
		t.Fatal(err)
	}
	if st.RepairVisitCost != 2500 {
		t.Fatalf("state pinned cost %d, want 2500", st.RepairVisitCost)
	}
	// A resumed run rebuilt with a cheaper config must not exceed the guard.
	r.Config.RepairVisitCost = 100
	if _, err := r.Resume(context.Background(), st, NoControl{}); err != nil {
		t.Fatal(err)
	}
	if !st.Failed || st.Limit != "cost" || st.RepairVisits != 2 || st.RepairCost != 5000 {
		t.Fatalf("state %+v, want failed cost after 2 repairs at 2500", st)
	}
}

// S7: the review policy is recorded in the state at Start, so the scenario's
// "no person asked / no reviewer session" assertions observe a real field.
func TestStartRecordsTheReviewPolicy(t *testing.T) {
	t.Parallel()
	r := bootstrapRunner(&fakeModel{plan: planOf("store")}, &fakeSandbox{}, &fakeGit{}, DefaultGuards())
	r.Config.Policy = Policy{AsksPerson: true, ReviewerSessions: 1}
	st, err := r.Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.PersonAsked {
		t.Fatal("state does not record that a person was asked")
	}
	if st.ReviewerSessions != 1 {
		t.Fatalf("state records %d reviewer sessions, want 1", st.ReviewerSessions)
	}
}

func TestIterateCommitsOnANewBranch(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("parser")}
	git := &fakeGit{}
	r := New(Config{Flow: FlowIterate, Version: "v1", Prompts: "v1/prompts", Evals: "v1/evals", Guards: DefaultGuards()}, model, &fakeSandbox{}, git)
	r.IDs = fixedID("run-test")
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
	model := &fakeModel{plan: planOf("store", "api")}
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
	model := &fakeModel{plan: planOf("store")}
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

// S2: a run's id is unique across processes, and Start refuses incomplete input
// before it calls a port.
func TestStartRejectsIncompleteInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		flow string
		in   Input
	}{
		{name: "bootstrap without a brief", flow: FlowBootstrap, in: Input{}},
		{name: "iterate without a repository", flow: FlowIterate, in: Input{BaseBranch: "main"}},
		{name: "iterate without a base branch", flow: FlowIterate, in: Input{Repo: "acme/app"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{plan: planOf("store")}
			git := &fakeGit{}
			r := New(Config{Flow: tc.flow, Version: "v1", Prompts: "v1/prompts", Evals: "v1/evals", Guards: DefaultGuards()}, model, &fakeSandbox{}, git)
			_, err := r.Start(context.Background(), tc.in, NoControl{})
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if len(model.requests) != 0 {
				t.Fatalf("the model was called %d times, want 0", len(model.requests))
			}
			if len(git.commits) != 0 {
				t.Fatalf("the git host was called %d times, want 0", len(git.commits))
			}
		})
	}
}

func TestRunIDsComeFromTheInjectedSource(t *testing.T) {
	t.Parallel()
	ids := []string{"run-a", "run-b"}
	next := 0
	r := bootstrapRunner(&fakeModel{plan: planOf("store")}, &fakeSandbox{}, &fakeGit{}, DefaultGuards())
	r.IDs = func() (string, error) {
		id := ids[next]
		next++
		return id, nil
	}
	first, err := r.Start(context.Background(), Input{Brief: "invoices"}, stopAfter{StagePlan})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Start(context.Background(), Input{Brief: "invoices"}, stopAfter{StagePlan})
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != "run-a" || second.RunID != "run-b" {
		t.Fatalf("run ids %q and %q, want run-a and run-b", first.RunID, second.RunID)
	}
}

// S3: stage requests carry the repository and the accumulated workspace, and
// every response is decoded into a fresh value.
func TestPlanRequestCarriesTheRepository(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("parser")}
	r := New(Config{Flow: FlowIterate, Version: "v1", Prompts: "v1/prompts", Evals: "v1/evals", Guards: DefaultGuards()}, model, &fakeSandbox{}, &fakeGit{})
	r.IDs = fixedID("run-test")
	if _, err := r.Start(context.Background(), Input{Repo: "acme/app", BaseBranch: "release/v1", ChangeRequest: "add a command"}, NoControl{}); err != nil {
		t.Fatal(err)
	}
	req := model.request("planner", 0)
	if req == nil {
		t.Fatal("the planner was not called")
	}
	var in struct {
		Repo       string `json:"repo"`
		BaseBranch string `json:"base_branch"`
	}
	if err := json.Unmarshal(req.Input, &in); err != nil {
		t.Fatal(err)
	}
	if in.Repo != "acme/app" || in.BaseBranch != "release/v1" {
		t.Fatalf("plan input %+v, want acme/app and release/v1", in)
	}
}

func TestImplementSeesTheAccumulatedWorkspace(t *testing.T) {
	t.Parallel()
	model := &fakeModel{
		plan: planOf("store", "api"),
		moduleWS: map[string]Workspace{
			"store": {ID: "ws-store", Files: map[string]string{"store.go": "store"}},
			"api":   {ID: "ws-api", Files: map[string]string{"api.go": "api"}},
		},
	}
	git := &fakeGit{}
	if _, err := bootstrapRunner(model, &fakeSandbox{}, git, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{}); err != nil {
		t.Fatal(err)
	}
	second := model.request("implement_module", 1)
	if second == nil {
		t.Fatal("the second module was not implemented")
	}
	var in struct {
		Module    string    `json:"module"`
		Workspace Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(second.Input, &in); err != nil {
		t.Fatal(err)
	}
	if in.Module != "api" || in.Workspace.Files["store.go"] != "store" {
		t.Fatalf("second implement input %+v, want the store workspace", in)
	}
	if len(git.commits) != 1 || git.commits[0].Workspace.Files["store.go"] != "store" || git.commits[0].Workspace.Files["api.go"] != "api" {
		t.Fatalf("committed workspace %+v, want both modules", git.commits)
	}
}

func TestModelNullResponsesAreRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     map[string]json.RawMessage
		failing bool
	}{
		{name: "planner", raw: map[string]json.RawMessage{"planner": json.RawMessage("null")}},
		{name: "planner with no modules", raw: map[string]json.RawMessage{"planner": json.RawMessage(`{"modules":[]}`)}},
		{name: "planner with no tests", raw: map[string]json.RawMessage{"planner": json.RawMessage(`{"modules":[{"name":"store"}]}`)}},
		{name: "reviewer with an empty finding", raw: map[string]json.RawMessage{"reviewer": json.RawMessage(`{"findings":[{}]}`)}},
		{name: "implementer", raw: map[string]json.RawMessage{"implement_module": json.RawMessage("null")}},
		{name: "repair", raw: map[string]json.RawMessage{"repair": json.RawMessage("null")}, failing: true},
		{name: "reviewer", raw: map[string]json.RawMessage{"reviewer": json.RawMessage("null")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{plan: planOf("store"), raw: tc.raw}
			sandbox := &fakeSandbox{}
			if tc.failing {
				sandbox.results = []TestResult{{Failures: []string{"tests fail"}}}
			}
			git := &fakeGit{}
			_, err := bootstrapRunner(model, sandbox, git, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if len(git.commits) != 0 {
				t.Fatalf("the git host was called %d times, want 0", len(git.commits))
			}
		})
	}
}

func TestReviewerEmptyFindingsIsAnAnswer(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("store"), raw: map[string]json.RawMessage{"reviewer": json.RawMessage(`{"findings":[]}`)}}
	git := &fakeGit{}
	if _, err := bootstrapRunner(model, &fakeSandbox{}, git, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{}); err != nil {
		t.Fatal(err)
	}
	if len(git.commits) != 1 {
		t.Fatalf("commits %d, want 1", len(git.commits))
	}
}

func TestReviewerWithoutAFindingsFieldIsRejected(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("store"), raw: map[string]json.RawMessage{"reviewer": json.RawMessage(`{}`)}}
	_, err := bootstrapRunner(model, &fakeSandbox{}, &fakeGit{}, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

// S4: verify runs exactly the plan's tests, in plan order.
func TestVerifyRunsExactlyThePlansTests(t *testing.T) {
	t.Parallel()
	plan := Plan{Modules: []Module{{Name: "store"}}, Tests: []string{"go test ./store", "go test ./api"}}
	sandbox := &fakeSandbox{}
	if _, err := bootstrapRunner(&fakeModel{plan: plan}, sandbox, &fakeGit{}, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.commands) != 2 || sandbox.commands[0] != plan.Tests[0] || sandbox.commands[1] != plan.Tests[1] {
		t.Fatalf("sandbox commands %v, want %v", sandbox.commands, plan.Tests)
	}
}

func TestVerifyAggregatesTheFailingTests(t *testing.T) {
	t.Parallel()
	plan := Plan{Modules: []Module{{Name: "store"}}, Tests: []string{"go test ./store", "go test ./api"}}
	model := &fakeModel{plan: plan}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"store fails"}}, {Failures: []string{"api fails"}}, {}, {}}}
	st, err := bootstrapRunner(model, sandbox, &fakeGit{}, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if err != nil {
		t.Fatal(err)
	}
	repair := model.request("repair", 0)
	if repair == nil {
		t.Fatal("the failing tests did not reach repair")
	}
	var in struct {
		Failures []string `json:"failures"`
	}
	if err := json.Unmarshal(repair.Input, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Failures) != 2 || in.Failures[0] != "store fails" || in.Failures[1] != "api fails" {
		t.Fatalf("repair failures %v, want both", in.Failures)
	}
	if !st.Done || st.Failed {
		t.Fatalf("state %+v, want a completed run", st)
	}
}

// S5b: an ambiguous commit failure is retried with the same idempotency key,
// and the host commits once.
type ambiguousGit struct {
	commits   int
	calls     []Commit
	committed map[string]bool
}

func (g *ambiguousGit) Commit(_ context.Context, c Commit) error {
	g.calls = append(g.calls, c)
	if g.committed == nil {
		g.committed = map[string]bool{}
	}
	if g.committed[c.Key] {
		return nil
	}
	g.committed[c.Key] = true
	g.commits++
	return errors.New("connection reset after the commit")
}

func TestCommitRetryUsesTheSameIdempotencyKey(t *testing.T) {
	t.Parallel()
	git := &ambiguousGit{}
	r := bootstrapRunner(&fakeModel{plan: planOf("store")}, &fakeSandbox{}, git, DefaultGuards())
	st, err := r.Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if err == nil {
		t.Fatal("the first commit error was swallowed")
	}
	if st.Done || !st.CommitAttempted || st.CommitKey == "" {
		t.Fatalf("state %+v, want a recorded commit attempt", st)
	}
	if _, err := r.Resume(context.Background(), st, NoControl{}); err != nil {
		t.Fatal(err)
	}
	if git.commits != 1 || len(git.calls) != 2 {
		t.Fatalf("commits %d calls %d, want 1 and 2", git.commits, len(git.calls))
	}
	if git.calls[0].Key == "" || git.calls[0].Key != git.calls[1].Key {
		t.Fatalf("keys %q and %q, want one stable key", git.calls[0].Key, git.calls[1].Key)
	}
	if !st.Done || st.Failed {
		t.Fatalf("state %+v, want a completed run", st)
	}
}

// S6: a malformed response is never free, and a plan over the module cap is
// refused before any implement call.
func TestRepairVisitCountsBeforeParsing(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("store"), raw: map[string]json.RawMessage{"repair": json.RawMessage("null")}}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"tests fail"}}}}
	st, err := bootstrapRunner(model, sandbox, &fakeGit{}, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if st.RepairVisits != 1 || st.RepairCost != 1000 {
		t.Fatalf("repair visits %d cost %d, want 1 and 1000", st.RepairVisits, st.RepairCost)
	}
}

func TestSelfReviewVisitCountsBeforeParsing(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("store"), raw: map[string]json.RawMessage{"reviewer": json.RawMessage("null")}}
	st, err := bootstrapRunner(model, &fakeSandbox{}, &fakeGit{}, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if st.SelfReviewVisits != 1 {
		t.Fatalf("self-review visits %d, want 1", st.SelfReviewVisits)
	}
}

func TestPlanModuleCap(t *testing.T) {
	t.Parallel()
	model := &fakeModel{plan: planOf("a", "b", "c")}
	r := bootstrapRunner(model, &fakeSandbox{}, &fakeGit{}, DefaultGuards())
	r.Config.MaxModules = 2
	_, err := r.Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if got := model.count("implement_module"); got != 0 {
		t.Fatalf("implement ran %d times, want 0", got)
	}
}

// S12: commit input is validated before it reaches the host.
func TestValidateCommit(t *testing.T) {
	t.Parallel()
	valid := Commit{
		Stage: StageCommit, Repo: "acme/app", Branch: "codegen/run-1", From: "release/v1",
		Workspace: Workspace{ID: "ws", Files: map[string]string{"cmd/main.go": "x"}},
	}
	if err := validateCommit(valid); err != nil {
		t.Fatalf("valid commit rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Commit)
	}{
		{name: "absolute path", mutate: func(c *Commit) { c.Workspace.Files = map[string]string{"/etc/passwd": "x"} }},
		{name: "parent path", mutate: func(c *Commit) { c.Workspace.Files = map[string]string{"../etc/passwd": "x"} }},
		{name: "empty path", mutate: func(c *Commit) { c.Workspace.Files = map[string]string{"": "x"} }},
		{name: "backslash path", mutate: func(c *Commit) { c.Workspace.Files = map[string]string{`cmd\main.go`: "x"} }},
		{name: "empty repo", mutate: func(c *Commit) { c.Repo = "" }},
		{name: "repo with a space", mutate: func(c *Commit) { c.Repo = "acme app" }},
		{name: "repo with a parent segment", mutate: func(c *Commit) { c.Repo = "../etc/passwd" }},
		{name: "branch with a double dot", mutate: func(c *Commit) { c.Branch = "codegen/../main" }},
		{name: "branch with a space", mutate: func(c *Commit) { c.Branch = "codegen/run 1" }},
		{name: "base ref with a double dot", mutate: func(c *Commit) { c.From = "release..v1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commit := valid
			commit.Workspace.Files = map[string]string{"cmd/main.go": "x"}
			tc.mutate(&commit)
			if err := validateCommit(commit); core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
		})
	}
}

// EP36-F2: an escaping workspace key is rejected when the model returns it,
// before verify hands the workspace to the sandbox and before the git host.
func TestUnsafeWorkspaceIsRejectedBeforeTheSandbox(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ws   Workspace
	}{
		{name: "parent path", ws: Workspace{ID: "ws-store", Files: map[string]string{"../etc/passwd": "x"}}},
		{name: "dirty spelling", ws: Workspace{ID: "ws-store", Files: map[string]string{"foo/../bar": "x"}}},
		{name: "absolute path", ws: Workspace{ID: "ws-store", Files: map[string]string{"/etc/passwd": "x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{plan: planOf("store"), moduleWS: map[string]Workspace{"store": tc.ws}}
			sandbox := &fakeSandbox{}
			git := &fakeGit{}
			_, err := bootstrapRunner(model, sandbox, git, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if len(sandbox.calls) != 0 {
				t.Fatalf("the sandbox was called %d times, want 0", len(sandbox.calls))
			}
			if len(git.commits) != 0 {
				t.Fatalf("the git host was called %d times, want 0", len(git.commits))
			}
		})
	}
}

// EP36-F2: the same check applies to a repair workspace, which replaces the
// run's workspace and is verified next.
func TestUnsafeRepairWorkspaceIsRejectedBeforeTheSandbox(t *testing.T) {
	t.Parallel()
	model := &fakeModel{
		plan:    planOf("store"),
		repairs: []Workspace{{ID: "ws-repair", Files: map[string]string{"foo/../bar": "x"}}},
	}
	sandbox := &fakeSandbox{results: []TestResult{{Failures: []string{"tests fail"}}}}
	_, err := bootstrapRunner(model, sandbox, &fakeGit{}, DefaultGuards()).Start(context.Background(), Input{Brief: "invoices"}, NoControl{})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if len(sandbox.calls) != 1 {
		t.Fatalf("the sandbox was called %d times, want only the first verify", len(sandbox.calls))
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
