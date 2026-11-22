package ir

import (
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/flow"
	flowir "github.com/epzero/basamento/flow/ir"
)

// Stage ids of the loop, in the IR's declared order.
const (
	PlanStages      = "plan"
	ImplementStages = "implement"
	ImplementChild  = "implement_module"
	VerifyStage     = "verify"
	VerifyChild     = "verify_test"
	RepairStage     = "repair"
	SelfReviewStage = "self-review"
	CommitStage     = "commit"
	VersionCompile  = "compile"
	VersionEval     = "eval"
	VersionReview   = "review"
)

// Definition is one preset version's flows, keyed by flow name.
type Definition map[string]*flow.IR

// All returns the three flows of a version.
func All() Definition {
	return Definition{
		"codegen.bootstrap": Bootstrap(),
		"codegen.iterate":   Iterate(),
		"codegen.version":   Version(),
	}
}

// Bootstrap is the codegen.bootstrap flow: a brief becomes a repository with
// specs and tests, and one commit lands on the new repository's default branch.
func Bootstrap() *flow.IR {
	return loop("codegen.bootstrap", PlanStages)
}

// Iterate is the codegen.iterate flow: a change request applies to an existing
// repository and lands on a new branch.
func Iterate() *flow.IR {
	return loop("codegen.iterate", PlanStages)
}

// Version is the codegen.version flow: compile, eval and a person's review of a
// proposed version. It uses a gate node and records no accepted version before
// the gate is resolved.
func Version() *flow.IR {
	return &flow.IR{
		Name:    "codegen.version",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"compile":{"type":"object"},"eval":{"type":"object"},` +
				`"review":{"type":"object"}}}`),
		},
		Nodes: map[string]flow.Node{
			VersionCompile: {Kind: "task", Out: "compile", Spec: json.RawMessage(`{"task":"compile","prompt":"prompts/version.md"}`)},
			VersionEval:    {Kind: "task", Out: "eval", Spec: json.RawMessage(`{"task":"eval","prompt":"prompts/version.md"}`)},
			VersionReview: {
				Kind: "gate",
				Out:  "review",
				Spec: json.RawMessage(`{"schema":{"type":"object","properties":{"decision":{"enum":["approved","rejected"]}},"required":["decision"]}}`),
			},
		},
		Edges: []flow.Edge{
			{From: flowir.Start, To: VersionCompile},
			{From: VersionCompile, To: VersionEval},
			{From: VersionEval, To: VersionReview},
			{From: VersionReview, To: flowir.Fail},
		},
	}
}

// loop builds one run flow. Both flows share the loop's nodes and guards; the
// first stage is plan for both, and the commit stage's target differs at run
// time (a new repository's default branch, or codegen/<run id>).
func loop(name string, first string) *flow.IR {
	repairGuard := &flow.Guard{MaxVisits: 3, CostMpts: 5000}
	reviewGuard := &flow.Guard{MaxVisits: 3}
	return &flow.IR{
		Name:    name,
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"plan":{"type":"object"},"workspace":{"type":"object"},` +
				`"tests":{"type":"object"},"findings":{"type":"object"}}}`),
		},
		Nodes: map[string]flow.Node{
			PlanStages: {
				Kind: "agent",
				Out:  "plan",
				Spec: json.RawMessage(`{"agent":"planner","model":"codegen","prompt":"prompts/planner.md","output_schema":"plan"}`),
			},
			ImplementStages: {
				Kind: "map",
				Out:  "workspace",
				Spec: json.RawMessage(`{"over":"state.plan.modules","max_parallel":0,"node":"` + ImplementChild + `"}`),
			},
			ImplementChild: {
				Kind: "agent",
				Out:  "workspace",
				Spec: json.RawMessage(`{"agent":"implement_module","model":"codegen","prompt":"prompts/implement.md","output_schema":"workspace"}`),
			},
			VerifyStage: {
				Kind: "map",
				Out:  "tests",
				Spec: json.RawMessage(`{"over":"state.plan.tests","max_parallel":0,"node":"` + VerifyChild + `"}`),
			},
			VerifyChild: {
				Kind: "tool",
				Out:  "tests",
				Spec: json.RawMessage(`{"tool":"run-tests","args":[]}`),
			},
			RepairStage: {
				Kind:  "agent",
				Out:   "workspace",
				Guard: repairGuard,
				Spec:  json.RawMessage(`{"agent":"repair","model":"codegen","prompt":"prompts/repair.md","output_schema":"workspace"}`),
			},
			SelfReviewStage: {
				Kind:  "agent",
				Out:   "findings",
				Guard: reviewGuard,
				Spec:  json.RawMessage(`{"agent":"reviewer","model":"codegen","prompt":"prompts/reviewer.md","output_schema":"findings"}`),
			},
			CommitStage: {
				Kind: "tool",
				Spec: json.RawMessage(`{"tool":"commit","prompt":"prompts/commit.md"}`),
			},
		},
		Edges: []flow.Edge{
			{From: flowir.Start, To: PlanStages},
			{From: PlanStages, To: ImplementStages},
			{From: ImplementStages, To: ImplementChild},
			{From: ImplementChild, To: VerifyStage},
			{From: VerifyStage, To: VerifyChild},
			{From: VerifyChild, To: SelfReviewStage, When: `size(state.tests.failures) == 0`},
			{From: VerifyChild, To: RepairStage},
			{From: RepairStage, To: VerifyStage},
			{From: SelfReviewStage, To: CommitStage, When: `size(state.findings.findings) == 0`},
			{From: SelfReviewStage, To: RepairStage},
		},
	}
}

// VerifyNodes returns one tool node per plan test, in plan order. Each node
// carries its command as literal spec.args: the host reads spec.tool and
// delivers the args, never a "command" field.
func VerifyNodes(tests []string) map[string]flow.Node {
	nodes := make(map[string]flow.Node, len(tests))
	for i, test := range tests {
		nodes[fmt.Sprintf("%s_%d", VerifyChild, i+1)] = VerifyNode(test)
	}
	return nodes
}

// VerifyNode returns the tool node that runs one plan test.
func VerifyNode(test string) flow.Node {
	spec, err := json.Marshal(map[string]any{"tool": "run-tests", "args": []string{test}})
	if err != nil {
		panic("codegen ir: verify node: " + err.Error())
	}
	return flow.Node{Kind: "tool", Out: "tests", Spec: spec}
}

// ExpectedStages is the order the Runner executes for both run flows. It mirrors
// the IR above and Definition.Validate checks that every stage exists.
func ExpectedStages() []string {
	return []string{PlanStages, ImplementStages, VerifyStage, RepairStage, SelfReviewStage, CommitStage}
}

// Canonical returns the canonical JSON of each flow, keyed by flow name.
func (d Definition) Canonical() (map[string][]byte, error) {
	out := make(map[string][]byte, len(d))
	for name, def := range d {
		if def == nil {
			return nil, fmt.Errorf("codegen ir: flow %s is nil", name)
		}
		raw, err := flowir.Canonical(def)
		if err != nil {
			return nil, err
		}
		out[name] = raw
	}
	return out, nil
}
