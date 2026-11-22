package codegen

import (
	"context"
	"encoding/json"
)

// Stage is one stage of the codegen loop. The stage log a run exposes is a
// sequence of these values.
type Stage string

// The stages of both codegen flows, in the order the IR declares them.
const (
	StagePlan       Stage = "plan"
	StageImplement  Stage = "implement"
	StageVerify     Stage = "verify"
	StageRepair     Stage = "repair"
	StageSelfReview Stage = "self-review"
	StageCommit     Stage = "commit"
)

// LoopStages is the stage order the Runner implements. The IR declares the
// same order and Compile cross-checks the two (definition.go).
func LoopStages() []Stage {
	return []Stage{StagePlan, StageImplement, StageVerify, StageRepair, StageSelfReview, StageCommit}
}

// Flows the preset defines.
const (
	FlowBootstrap = "codegen.bootstrap"
	FlowIterate   = "codegen.iterate"
	FlowVersion   = "codegen.version"
)

// Guard bounds a stage's visits and spend. It mirrors flow.Guard: zero means no
// bound.
type Guard struct {
	MaxVisits int   `json:"max_visits,omitempty"`
	CostMpts  int64 `json:"cost_mpts,omitempty"`
}

// Guards are the loop's two guards (specs/presets/design.md §1): the repair
// stage's visits and cost, and the self-review stage's visits.
type Guards struct {
	Repair     Guard `json:"repair"`
	SelfReview Guard `json:"self_review"`
}

// DefaultGuards returns the guards in the scenarios' Background: a repair guard
// of 3 visits and 5000 milli-points and a self-review guard of 3 visits.
func DefaultGuards() Guards {
	return Guards{
		Repair:     Guard{MaxVisits: 3, CostMpts: 5000},
		SelfReview: Guard{MaxVisits: 3},
	}
}

// Module is one module of a plan.
type Module struct {
	Name string `json:"name"`
}

// Plan is the planner's structured output. Tests are the sandbox commands the
// verify stage runs, in plan order (REQ-PRESET-005).
type Plan struct {
	Modules []Module `json:"modules"`
	Tests   []string `json:"tests"`
}

// Workspace is the files a model stage returned. The sandbox tests it and the
// commit writes the workspace the last verification tested.
type Workspace struct {
	ID    string            `json:"id"`
	Files map[string]string `json:"files,omitempty"`
}

// Finding is one self-review finding.
type Finding struct {
	Message string `json:"message"`
}

// Findings is the reviewer's structured output.
type Findings struct {
	Findings []Finding `json:"findings"`
}

// TestResult is one run of the plan's tests in the sandbox. An empty Failures
// list means the tests passed.
type TestResult struct {
	Failures []string `json:"failures,omitempty"`
}

// Passed reports whether the plan's tests passed.
func (r TestResult) Passed() bool { return len(r.Failures) == 0 }

// Request is one model request. Agent is the flow's agent ("planner",
// "implement_module", "repair" or "reviewer"), Module is set for an implement
// request, and Stage is the stage that made it.
type Request struct {
	Flow          string          `json:"flow"`
	Stage         Stage           `json:"stage"`
	Agent         string          `json:"agent"`
	Module        string          `json:"module,omitempty"`
	PromptVersion string          `json:"prompt_version"`
	Input         json.RawMessage `json:"input,omitempty"`
}

// Model is the codegen model port. Complete answers one agent request with its
// structured output: a Plan, a Workspace or Findings, decided by Agent.
type Model interface {
	Complete(ctx context.Context, req Request) (json.RawMessage, error)
}

// Sandbox tests one workspace with one of the plan's test commands; the
// simulated sandbox records the command with the workspace it received.
type Sandbox interface {
	RunTests(ctx context.Context, workspace Workspace, command string) (TestResult, error)
}

// Commit is one commit request. NewRepo asks for the first commit of a new
// repository on its default branch. Otherwise Branch is created from From when
// it does not exist, and the commit lands on it; From may be the default
// branch, which is then left untouched. Key is the run's stable idempotency
// key for this stage, so a retried commit is the same commit.
type Commit struct {
	Stage     Stage     `json:"stage"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch,omitempty"`
	From      string    `json:"from,omitempty"`
	NewRepo   bool      `json:"new_repo,omitempty"`
	Key       string    `json:"idempotency_key,omitempty"`
	Workspace Workspace `json:"workspace"`
}

// GitHost is the git host's write side. The codegen loop calls it only from the
// commit stage (REQ-PRESET-011, REQ-PRESET-013).
type GitHost interface {
	Commit(ctx context.Context, commit Commit) error
}
