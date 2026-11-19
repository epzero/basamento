package codegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/epzero/basamento/core"
)

// ErrStop is returned by a Control to pause a run after a named stage. The
// state keeps its completed stages, so a resume never repeats them
// (REQ-PRESET-003).
var ErrStop = errors.New("codegen: stop after stage")

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

// Config is a run's pinned definition and guards.
type Config struct {
	Flow            string
	Version         string
	Prompts         string
	Evals           string
	Guards          Guards
	RepairVisitCost int64 // milli-points per repair visit; 0 = 1000
}

// Event is one progress event a run exposes.
type Event struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data,omitempty"`
}

// State is a run's serializable state. Checkpoints, the stage log and the
// pinned version live here, so a resume continues from the same point.
type State struct {
	Flow    string
	Version string
	Prompts string
	Evals   string
	Guards  Guards

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

	runs int
}

// New returns a runner over the three ports.
func New(cfg Config, model Model, sandbox Sandbox, git GitHost) *Runner {
	if cfg.RepairVisitCost <= 0 {
		cfg.RepairVisitCost = 1000
	}
	return &Runner{Config: cfg, Model: model, Sandbox: sandbox, GitHost: git}
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
	if ctl == nil {
		ctl = NoControl{}
	}
	r.runs++
	st := &State{
		Flow:        r.Config.Flow,
		Version:     r.Config.Version,
		Prompts:     r.Config.Prompts,
		Evals:       r.Config.Evals,
		Guards:      r.Config.Guards,
		RunID:       fmt.Sprintf("run-%d", r.runs),
		Input:       in,
		Checkpoints: map[Stage]bool{},
		Next:        StagePlan,
		Started:     true,
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
			}),
		})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(resp, &st.Plan); err != nil {
			return invalid("codegen: planner returned an invalid plan: %s", err.Error())
		}
		st.complete(stage)
		st.Next = StageImplement

	case StageImplement:
		for _, module := range st.Plan.Modules {
			resp, err := r.Model.Complete(ctx, Request{
				Flow: st.Flow, Stage: stage, Agent: "implement_module",
				Module: module.Name, PromptVersion: st.Prompts,
				Input: mustJSON(map[string]string{"module": module.Name}),
			})
			if err != nil {
				return err
			}
			if err := json.Unmarshal(resp, &st.Workspace); err != nil {
				return invalid("codegen: implementer returned an invalid workspace: %s", err.Error())
			}
		}
		st.complete(stage)
		st.Next = StageVerify

	case StageVerify:
		result, err := r.Sandbox.RunTests(ctx, st.Workspace, "go test ./...")
		if err != nil {
			return err
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
		input := map[string]any{}
		if len(st.PendingFindings) > 0 {
			input["findings"] = st.PendingFindings
		} else {
			input["failures"] = st.LastTests.Failures
		}
		resp, err := r.Model.Complete(ctx, Request{
			Flow: st.Flow, Stage: stage, Agent: "repair",
			PromptVersion: st.Prompts, Input: mustJSON(input),
		})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(resp, &st.Workspace); err != nil {
			return invalid("codegen: repair returned an invalid workspace: %s", err.Error())
		}
		st.RepairVisits++
		st.RepairCost += r.Config.RepairVisitCost
		st.PendingFindings = nil
		st.complete(stage)
		st.Next = StageVerify

	case StageSelfReview:
		if st.Guards.SelfReview.MaxVisits > 0 && st.SelfReviewVisits >= st.Guards.SelfReview.MaxVisits {
			st.fail("visits")
			return nil
		}
		resp, err := r.Model.Complete(ctx, Request{
			Flow: st.Flow, Stage: stage, Agent: "reviewer",
			PromptVersion: st.Prompts, Input: mustJSON(map[string]any{
				"workspace": st.LastVerified, "tests": st.LastTests,
			}),
		})
		if err != nil {
			return err
		}
		var findings Findings
		if err := json.Unmarshal(resp, &findings); err != nil {
			return invalid("codegen: reviewer returned invalid findings: %s", err.Error())
		}
		st.SelfReviewVisits++
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
		commit := Commit{Stage: stage, Workspace: st.LastVerified}
		if st.Flow == FlowBootstrap {
			commit.Repo = st.Input.Brief
			commit.NewRepo = true
		} else {
			commit.Repo = st.Input.Repo
			commit.Branch = "codegen/" + st.RunID
			commit.From = st.Input.BaseBranch
		}
		if err := r.GitHost.Commit(ctx, commit); err != nil {
			return err
		}
		st.complete(stage)
		st.Next = ""
		st.Done = true

	default:
		return invalid("codegen: unknown stage %q", stage)
	}
	return nil
}

// repairLimit reports the guard a repair would exceed, or "" when it may run.
func (r *Runner) repairLimit(st *State) string {
	if st.Guards.Repair.MaxVisits > 0 && st.RepairVisits >= st.Guards.Repair.MaxVisits {
		return "visits"
	}
	if st.Guards.Repair.CostMpts > 0 && st.RepairCost+r.Config.RepairVisitCost > st.Guards.Repair.CostMpts {
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
