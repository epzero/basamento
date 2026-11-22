package codegen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/workspace"
)

// ErrStop is returned by a Control to pause a run after a named stage. The
// state keeps its completed stages, so a resume never repeats them
// (REQ-PRESET-003).
var ErrStop = errors.New("codegen: stop after stage")

// DefaultMaxModules is the largest plan the runner accepts when
// Config.MaxModules is zero.
const DefaultMaxModules = 8

// Control lets a caller hold a run before a stage, or stop it after one. It is
// how the gate exercises resume without a wall clock or a sleep.
type Control interface {
	BeforeStage(ctx context.Context, stage Stage) error
	AfterStage(ctx context.Context, stage Stage) error
}

// NoControl runs a run to its end.
type NoControl struct{}

// BeforeStage does nothing.
func (NoControl) BeforeStage(context.Context, Stage) error { return nil }

// AfterStage does nothing.
func (NoControl) AfterStage(context.Context, Stage) error { return nil }

// Input is what starts a run. Bootstrap uses Brief; iterate uses Repo,
// BaseBranch and ChangeRequest.
type Input struct {
	Brief         string `json:"brief,omitempty"`
	ChangeRequest string `json:"change_request,omitempty"`
	Repo          string `json:"repo,omitempty"`
	BaseBranch    string `json:"base_branch,omitempty"`
}

// Policy is the run's review policy: the loop's real behavior, recorded in
// the state at Start so a scenario can observe it. The codegen loop asks no
// person and starts no reviewer session before the commit (REQ-PRESET-008).
type Policy struct {
	AsksPerson       bool
	ReviewerSessions int
}

// Config is a run's pinned definition and guards.
type Config struct {
	Flow            string
	Version         string
	Prompts         string
	Evals           string
	Guards          Guards
	Policy          Policy
	RepairVisitCost int64 // milli-points per repair visit; 0 = 1000
	MaxModules      int   // largest accepted plan; 0 = DefaultMaxModules
}

// Event is one progress event a run exposes.
type Event struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data,omitempty"`
}

// State is a run's serializable state. Checkpoints, the stage log and the
// pinned version live here, so a resume continues from the same point.
type State struct {
	Flow            string
	Version         string
	Prompts         string
	Evals           string
	Guards          Guards
	RepairVisitCost int64 // pinned at Start; a resume keeps it

	RunID string
	Input Input

	Plan            Plan
	Workspace       Workspace
	LastVerified    Workspace
	LastTests       TestResult
	PendingFindings []Finding

	StageLog         []Stage
	Checkpoints      map[Stage]bool
	RepairVisits     int
	RepairCost       int64
	SelfReviewVisits int
	Verifications    int
	Next             Stage

	CommitKey       string
	CommitAttempted bool
	Committed       bool

	Started  bool
	Done     bool
	Failed   bool
	Stopped  bool
	Code     core.Code
	Limit    string
	Failures []string

	PersonAsked      bool
	ReviewerSessions int

	Events []Event
}

// Runner executes the loop over the IR's stages with the injected ports.
type Runner struct {
	Config  Config
	Model   Model
	Sandbox Sandbox
	GitHost GitHost

	// IDs returns a new run id. New installs a crypto/rand source; a test
	// injects a deterministic one.
	IDs func() (string, error)
}

// New returns a runner over the three ports.
func New(cfg Config, model Model, sandbox Sandbox, git GitHost) *Runner {
	if cfg.RepairVisitCost <= 0 {
		cfg.RepairVisitCost = 1000
	}
	if cfg.MaxModules <= 0 {
		cfg.MaxModules = DefaultMaxModules
	}
	return &Runner{Config: cfg, Model: model, Sandbox: sandbox, GitHost: git, IDs: randomRunID}
}

// Start begins a run and executes it until it completes, fails or a Control
// stops it.
func (r *Runner) Start(ctx context.Context, in Input, ctl Control) (*State, error) {
	if r.Model == nil || r.Sandbox == nil || r.GitHost == nil {
		return nil, invalid("codegen: a run needs a model, a sandbox and a git host")
	}
	if r.Config.Flow != FlowBootstrap && r.Config.Flow != FlowIterate {
		return nil, invalid("codegen: unknown flow %q", r.Config.Flow)
	}
	switch r.Config.Flow {
	case FlowBootstrap:
		if strings.TrimSpace(in.Brief) == "" {
			return nil, invalid("codegen: a bootstrap run needs a brief")
		}
	case FlowIterate:
		if strings.TrimSpace(in.Repo) == "" {
			return nil, invalid("codegen: an iterate run needs a repository")
		}
		if strings.TrimSpace(in.BaseBranch) == "" {
			return nil, invalid("codegen: an iterate run needs a base branch")
		}
	}
	if ctl == nil {
		ctl = NoControl{}
	}
	ids := r.IDs
	if ids == nil {
		ids = randomRunID
	}
	id, err := ids()
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, invalid("codegen: the run id source returned an empty id")
	}
	st := &State{
		Flow:             r.Config.Flow,
		Version:          r.Config.Version,
		Prompts:          r.Config.Prompts,
		Evals:            r.Config.Evals,
		Guards:           r.Config.Guards,
		RepairVisitCost:  r.repairVisitCost(),
		PersonAsked:      r.Config.Policy.AsksPerson,
		ReviewerSessions: r.Config.Policy.ReviewerSessions,
		RunID:            id,
		Input:            in,
		Checkpoints:      map[Stage]bool{},
		Next:             StagePlan,
		Started:          true,
		Events: []Event{{
			Type: "run-started",
			Data: map[string]string{
				"flow":    r.Config.Flow,
				"version": r.Config.Version,
				"prompts": r.Config.Prompts,
				"evals":   r.Config.Evals,
			},
		}},
	}
	return r.execute(ctx, st, ctl)
}

// Resume continues a stopped run from its next stage.
func (r *Runner) Resume(ctx context.Context, st *State, ctl Control) (*State, error) {
	if st == nil {
		return nil, invalid("codegen: no run state to resume")
	}
	if ctl == nil {
		ctl = NoControl{}
	}
	st.Stopped = false
	return r.execute(ctx, st, ctl)
}

func (r *Runner) execute(ctx context.Context, st *State, ctl Control) (*State, error) {
	for !st.Done {
		if err := ctx.Err(); err != nil {
			return st, core.NewError(core.CodeCancelled, "codegen: run cancelled", nil)
		}
		stage := st.Next
		if stage == "" {
			st.Done = true
			break
		}
		if err := ctl.BeforeStage(ctx, stage); err != nil {
			if errors.Is(err, ErrStop) {
				st.Stopped = true
				return st, nil
			}
			return st, err
		}
		if err := r.stage(ctx, st, stage); err != nil {
			return st, err
		}
		if st.Done {
			break
		}
		if err := ctl.AfterStage(ctx, stage); err != nil {
			if errors.Is(err, ErrStop) {
				st.Stopped = true
				return st, nil
			}
			return st, err
		}
	}
	return st, nil
}

// stage runs one stage and sets the next one. A guard failure ends the run in
// the failed state and returns no error.
func (r *Runner) stage(ctx context.Context, st *State, stage Stage) error {
	switch stage {
	case StagePlan:
		resp, err := r.Model.Complete(ctx, Request{
			Flow: st.Flow, Stage: stage, Agent: "planner",
			PromptVersion: st.Prompts, Input: mustJSON(map[string]string{
				"brief": st.Input.Brief, "change_request": st.Input.ChangeRequest,
				"repo": st.Input.Repo, "base_branch": st.Input.BaseBranch,
			}),
		})
		if err != nil {
			return err
		}
		var plan Plan
		if err := json.Unmarshal(resp, &plan); err != nil {
			return invalid("codegen: planner returned an invalid plan: %s", err.Error())
		}
		if err := r.validatePlan(plan); err != nil {
			return err
		}
		st.Plan = plan
		st.complete(stage)
		st.Next = StageImplement

	case StageImplement:
		for _, module := range st.Plan.Modules {
			resp, err := r.Model.Complete(ctx, Request{
				Flow: st.Flow, Stage: stage, Agent: "implement_module",
				Module: module.Name, PromptVersion: st.Prompts,
				Input: mustJSON(map[string]any{"module": module.Name, "workspace": st.Workspace}),
			})
			if err != nil {
				return err
			}
			ws, err := decodeWorkspace(resp, "implementer")
			if err != nil {
				return err
			}
			merged, err := accumulateWorkspace(st.Workspace, ws)
			if err != nil {
				return err
			}
			st.Workspace = merged
		}
		st.complete(stage)
		st.Next = StageVerify

	case StageVerify:
		result := TestResult{}
		for _, command := range st.Plan.Tests {
			next, err := r.Sandbox.RunTests(ctx, st.Workspace, command)
			if err != nil {
				return err
			}
			result.Failures = append(result.Failures, next.Failures...)
		}
		st.Verifications++
		st.LastTests = result
		st.log(stage)
		if result.Passed() {
			st.LastVerified = st.Workspace
			st.checkpoint(stage)
			st.Next = StageSelfReview
		} else {
			st.Next = StageRepair
		}

	case StageRepair:
		if msg := r.repairLimit(st); msg != "" {
			st.fail(msg)
			return nil
		}
		input := map[string]any{"workspace": st.Workspace}
		if len(st.PendingFindings) > 0 {
			input["findings"] = st.PendingFindings
		} else {
			input["failures"] = st.LastTests.Failures
		}
		st.RepairVisits++
		st.RepairCost += st.repairVisitCost()
		resp, err := r.Model.Complete(ctx, Request{
			Flow: st.Flow, Stage: stage, Agent: "repair",
			PromptVersion: st.Prompts, Input: mustJSON(input),
		})
		if err != nil {
			return err
		}
		ws, err := decodeWorkspace(resp, "repair")
		if err != nil {
			return err
		}
		st.Workspace = ws
		st.PendingFindings = nil
		st.complete(stage)
		st.Next = StageVerify

	case StageSelfReview:
		if st.Guards.SelfReview.MaxVisits > 0 && st.SelfReviewVisits >= st.Guards.SelfReview.MaxVisits {
			st.fail("visits")
			return nil
		}
		st.SelfReviewVisits++
		resp, err := r.Model.Complete(ctx, Request{
			Flow: st.Flow, Stage: stage, Agent: "reviewer",
			PromptVersion: st.Prompts, Input: mustJSON(map[string]any{
				"workspace": st.LastVerified, "tests": st.LastTests,
			}),
		})
		if err != nil {
			return err
		}
		findings, err := decodeFindings(resp)
		if err != nil {
			return err
		}
		st.log(stage)
		if len(findings.Findings) > 0 {
			st.PendingFindings = findings.Findings
			if st.Guards.SelfReview.MaxVisits > 0 && st.SelfReviewVisits >= st.Guards.SelfReview.MaxVisits {
				st.fail("visits")
				return nil
			}
			st.Next = StageRepair
			return nil
		}
		st.checkpoint(stage)
		st.Next = StageCommit

	case StageCommit:
		key := commitKey(st, stage)
		commit := Commit{Stage: stage, Key: key, Workspace: st.LastVerified}
		if st.Flow == FlowBootstrap {
			commit.Repo = st.Input.Brief
			commit.NewRepo = true
		} else {
			commit.Repo = st.Input.Repo
			commit.Branch = "codegen/" + st.RunID
			commit.From = st.Input.BaseBranch
		}
		if err := validateCommit(commit); err != nil {
			return err
		}
		st.CommitKey = key
		st.CommitAttempted = true
		if err := r.GitHost.Commit(ctx, commit); err != nil {
			return err
		}
		st.Committed = true
		st.complete(stage)
		st.Next = ""
		st.Done = true

	default:
		return invalid("codegen: unknown stage %q", stage)
	}
	return nil
}

func (r *Runner) validatePlan(plan Plan) error {
	if len(plan.Modules) == 0 {
		return invalid("codegen: planner returned a plan with no modules")
	}
	if max := r.maxModules(); len(plan.Modules) > max {
		return invalid("codegen: plan has %d modules, the maximum is %d", len(plan.Modules), max)
	}
	for i, module := range plan.Modules {
		if strings.TrimSpace(module.Name) == "" {
			return invalid("codegen: plan module %d has no name", i+1)
		}
	}
	if len(plan.Tests) == 0 {
		return invalid("codegen: planner returned a plan with no tests")
	}
	for i, command := range plan.Tests {
		if strings.TrimSpace(command) == "" {
			return invalid("codegen: plan test %d is empty", i+1)
		}
	}
	return nil
}

func (r *Runner) maxModules() int {
	if r.Config.MaxModules > 0 {
		return r.Config.MaxModules
	}
	return DefaultMaxModules
}

// repairVisitCost returns the cost a run pins at Start. A zero config uses the
// documented default, so a resumed run never reads a rebuilt config.
func (r *Runner) repairVisitCost() int64 {
	if r.Config.RepairVisitCost > 0 {
		return r.Config.RepairVisitCost
	}
	return 1000
}

// repairVisitCost returns the cost pinned in the state at Start. A zero value
// is a state built before the pin (or a runner built without New) and uses the
// documented default.
func (st *State) repairVisitCost() int64 {
	if st.RepairVisitCost > 0 {
		return st.RepairVisitCost
	}
	return 1000
}

// repairLimit reports the guard a repair would exceed, or "" when it may run.
func (r *Runner) repairLimit(st *State) string {
	if st.Guards.Repair.MaxVisits > 0 && st.RepairVisits >= st.Guards.Repair.MaxVisits {
		return "visits"
	}
	if st.Guards.Repair.CostMpts > 0 && st.RepairCost+st.repairVisitCost() > st.Guards.Repair.CostMpts {
		return "cost"
	}
	return ""
}

// complete logs a stage and checkpoints it.
func (st *State) complete(stage Stage) {
	st.log(stage)
	st.checkpoint(stage)
}

func (st *State) log(stage Stage) {
	st.StageLog = append(st.StageLog, stage)
}

func (st *State) checkpoint(stage Stage) {
	if st.Checkpoints == nil {
		st.Checkpoints = map[Stage]bool{}
	}
	st.Checkpoints[stage] = true
}

func (st *State) fail(limit string) {
	st.Failed = true
	st.Done = true
	st.Code = core.CodeBudgetExceeded
	st.Limit = limit
	st.Failures = append([]string(nil), st.LastTests.Failures...)
	st.Next = ""
	st.Events = append(st.Events, Event{
		Type: "run-failed",
		Data: map[string]string{"code": string(core.CodeBudgetExceeded), "limit": limit},
	})
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("codegen: marshal: " + err.Error())
	}
	return raw
}

func randomRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "run-" + hex.EncodeToString(b[:]), nil
}

func commitKey(st *State, stage Stage) string {
	return st.RunID + ":" + string(stage)
}

// accumulateWorkspace merges one model's workspace into the run's. Every key
// is checked before it is merged, so a path that escapes the work directory
// never reaches the sandbox or a later stage (REQ-PRESET-005, 011).
func accumulateWorkspace(base, next Workspace) (Workspace, error) {
	if err := checkWorkspaceFiles(next, "workspace"); err != nil {
		return Workspace{}, err
	}
	if len(next.Files) > 0 {
		if base.Files == nil {
			base.Files = map[string]string{}
		}
		for name, body := range next.Files {
			base.Files[name] = body
		}
	}
	if next.ID != "" {
		base.ID = next.ID
	}
	return base, nil
}

func decodeWorkspace(resp json.RawMessage, who string) (Workspace, error) {
	var ws Workspace
	if err := json.Unmarshal(resp, &ws); err != nil {
		return Workspace{}, invalid("codegen: %s returned an invalid workspace: %s", who, err.Error())
	}
	if ws.ID == "" {
		return Workspace{}, invalid("codegen: %s returned a workspace with no id", who)
	}
	if err := checkWorkspaceFiles(ws, who); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

// checkWorkspaceFiles rejects a workspace key that is not a clean relative
// path, before the workspace is tested or merged. The commit repeats the check
// on what it writes; this one keeps an escaping key out of the sandbox.
func checkWorkspaceFiles(ws Workspace, who string) error {
	for name := range ws.Files {
		if !workspace.ValidPath(name) {
			return invalid("codegen: %s returned workspace path %q, which is not a clean relative path", who, name)
		}
	}
	return nil
}

func decodeFindings(resp json.RawMessage) (Findings, error) {
	var body struct {
		Findings *[]Finding `json:"findings"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		return Findings{}, invalid("codegen: reviewer returned invalid findings: %s", err.Error())
	}
	if body.Findings == nil {
		return Findings{}, invalid("codegen: reviewer returned no findings field")
	}
	for i, finding := range *body.Findings {
		if strings.TrimSpace(finding.Message) == "" {
			return Findings{}, invalid("codegen: reviewer finding %d has no message", i+1)
		}
	}
	return Findings{Findings: *body.Findings}, nil
}

// validateCommit refuses a commit the git host cannot safely receive: a
// workspace path that escapes the repository, a malformed repository name, or
// a malformed branch or base ref.
func validateCommit(c Commit) error {
	if !validRepo(c.Repo) {
		return invalid("codegen: commit repository %q is not valid", c.Repo)
	}
	if c.Branch != "" && !validRef(c.Branch) {
		return invalid("codegen: commit branch %q is not valid", c.Branch)
	}
	if c.From != "" && !validRef(c.From) {
		return invalid("codegen: commit base ref %q is not valid", c.From)
	}
	if !c.NewRepo && c.Branch == "" {
		return invalid("codegen: a commit to an existing repository needs a branch")
	}
	for name := range c.Workspace.Files {
		if !workspace.ValidPath(name) {
			return invalid("codegen: commit workspace path %q is not a clean relative path", name)
		}
	}
	return nil
}

func validRepo(repo string) bool {
	if repo == "" || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") || strings.ContainsAny(repo, " \t\r\n\\") {
		return false
	}
	for _, segment := range strings.Split(repo, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validRef(ref string) bool {
	if ref == "" || ref == "@" || strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return false
	}
	for _, r := range ref {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, segment := range strings.Split(ref, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") {
			return false
		}
	}
	return true
}
