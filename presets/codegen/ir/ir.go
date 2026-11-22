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
	VersionAccepted = "accepted"
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
// the gate is resolved. The approved edge reaches the accepted terminal node;
// the rejected edge reaches FAIL, so the IR states the lifecycle the registry
// implements (REQ-PRESET-016..018).
func Version() *flow.IR {
	return &flow.IR{
		Name:    "codegen.version",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"compile":{"type":"object"},"eval":{"type":"object"},` +
				`"review":{"type":"object"},"accepted":{"type":"object"}}}`),
		},
		Nodes: map[string]flow.Node{
			VersionCompile: {Kind: "task", Out: "compile", Spec: json.RawMessage(`{"task":"compile","prompt":"prompts/version.md"}`)},
			VersionEval:    {Kind: "task", Out: "eval", Spec: json.RawMessage(`{"task":"eval","prompt":"prompts/version.md"}`)},
			VersionReview: {
				Kind: "gate",
				Out:  "review",
				Spec: json.RawMessage(`{"schema":{"type":"object","properties":{"decision":{"enum":["approved","rejected"]}},"required":["decision"]}}`),
			},
			VersionAccepted: {Kind: "set", Out: "accepted"},
		},
		Edges: []flow.Edge{
			{From: flowir.Start, To: VersionCompile},
			{From: VersionCompile, To: VersionEval},
			{From: VersionEval, To: VersionReview},
			{From: VersionReview, To: VersionAccepted, When: `state.review.decision == "approved"`},
			{From: VersionReview, To: flowir.Fail, When: `state.review.decision == "rejected"`},
		},
	}
}

// repairGuard and reviewGuard return fresh guards: two flows must never share
// a mutable pointer.
func repairGuard() *flow.Guard { return &flow.Guard{MaxVisits: 3, CostMpts: 5000} }
func reviewGuard() *flow.Guard { return &flow.Guard{MaxVisits: 3} }

// LoopGuards returns the guards the loop declares, as values.
func LoopGuards() (repair, review flow.Guard) {
	return *repairGuard(), *reviewGuard()
}

// loop builds one run flow. Both flows share the loop's nodes and guards; the
// first stage is plan for both, and the commit stage's target differs at run
// time (a new repository's default branch, or codegen/<run id>).
func loop(name string, first string) *flow.IR {
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
				Spec: json.RawMessage(`{"tool":"run-tests"}`),
			},
			RepairStage: {
				Kind:  "agent",
				Out:   "workspace",
				Guard: repairGuard(),
				Spec:  json.RawMessage(`{"agent":"repair","model":"codegen","prompt":"prompts/repair.md","output_schema":"workspace"}`),
			},
			SelfReviewStage: {
				Kind:  "agent",
				Out:   "findings",
				Guard: reviewGuard(),
				Spec:  json.RawMessage(`{"agent":"reviewer","model":"codegen","prompt":"prompts/reviewer.md","output_schema":"findings"}`),
			},
			CommitStage: {
				Kind: "tool",
				Spec: json.RawMessage(`{"tool":"commit","prompt":"prompts/commit.md"}`),
			},
		},
		Edges: []flow.Edge{
			{From: flowir.Start, To: first},
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

// CheckVerify enforces the verify stage's declared shape: a map over
// state.plan.tests whose child is the run-tests tool node. The plan is a
// runtime output, so the static IR cannot hold one node per test; the map is
// how the flow declares one tool execution per test, in plan order. The map
// delivers each test as the child's input, and the host passes a tool node's
// input as its call args, so the child must not carry a static spec.args that
// would claim a different command. A run-tests node outside that map must
// carry its command as non-empty literal spec.args: nothing else supplies it,
// and an empty list would run the tool with no command.
func CheckVerify(def *flow.IR) error {
	stage, ok := def.Nodes[VerifyStage]
	if !ok {
		return fmt.Errorf("codegen ir: flow %q has no stage %q", def.Name, VerifyStage)
	}
	if stage.Kind != "map" {
		return fmt.Errorf("codegen ir: flow %q stage %q is %q, want map", def.Name, VerifyStage, stage.Kind)
	}
	var spec struct {
		Over        string `json:"over"`
		Node        string `json:"node"`
		MaxParallel *int   `json:"max_parallel"`
	}
	if err := json.Unmarshal(stage.Spec, &spec); err != nil {
		return fmt.Errorf("codegen ir: flow %q stage %q: %s", def.Name, VerifyStage, err.Error())
	}
	if spec.Over != "state.plan.tests" || spec.Node != VerifyChild || spec.MaxParallel == nil || *spec.MaxParallel != 0 {
		return fmt.Errorf("codegen ir: flow %q stage %q must map state.plan.tests over %q with max_parallel 0", def.Name, VerifyStage, VerifyChild)
	}
	child, ok := def.Nodes[VerifyChild]
	if !ok {
		return fmt.Errorf("codegen ir: flow %q has no verify child %q", def.Name, VerifyChild)
	}
	if child.Kind != "tool" {
		return fmt.Errorf("codegen ir: flow %q verify child %q is %q, want tool", def.Name, VerifyChild, child.Kind)
	}
	var childSpec map[string]json.RawMessage
	if err := json.Unmarshal(child.Spec, &childSpec); err != nil {
		return fmt.Errorf("codegen ir: flow %q verify child %q: %s", def.Name, VerifyChild, err.Error())
	}
	if string(childSpec["tool"]) != `"run-tests"` {
		return fmt.Errorf("codegen ir: flow %q verify child %q runs %s, want run-tests", def.Name, VerifyChild, childSpec["tool"])
	}
	if _, ok := childSpec["args"]; ok {
		return fmt.Errorf("codegen ir: flow %q verify child %q declares spec.args; the map supplies each test as the node input", def.Name, VerifyChild)
	}
	for id, node := range def.Nodes {
		if id == VerifyChild || node.Kind != "tool" {
			continue
		}
		var nodeSpec struct {
			Tool string   `json:"tool"`
			Args []string `json:"args"`
		}
		if err := json.Unmarshal(node.Spec, &nodeSpec); err != nil {
			return fmt.Errorf("codegen ir: flow %q node %q: %s", def.Name, id, err.Error())
		}
		if nodeSpec.Tool == "run-tests" && len(nodeSpec.Args) == 0 {
			return fmt.Errorf("codegen ir: flow %q node %q runs run-tests with empty or missing spec.args", def.Name, id)
		}
	}
	return nil
}

// ExpectedStages is the stage order the IR declares for both run flows. The
// Runner implements the loop; Compile cross-checks this order, the edges and
// the guards against the runner's (definition.go).
func ExpectedStages() []string {
	return []string{PlanStages, ImplementStages, VerifyStage, RepairStage, SelfReviewStage, CommitStage}
}

// CheckLoop verifies that def is the loop the Runner implements: the same
// stages, the same edges and conditions, and the same guards. The runner owns
// the behavior; this check keeps the declarative IR a faithful mirror.
func CheckLoop(def *flow.IR) error {
	if def == nil {
		return fmt.Errorf("codegen ir: nil flow")
	}
	if err := CheckVerify(def); err != nil {
		return err
	}
	want := []flow.Edge{
		{From: flowir.Start, To: PlanStages},
		{From: PlanStages, To: ImplementStages},
		{From: ImplementStages, To: ImplementChild},
		{From: ImplementChild, To: VerifyStage},
		{From: VerifyStage, To: VerifyChild},
		{From: VerifyChild, To: SelfReviewStage, When: "size(state.tests.failures) == 0"},
		{From: VerifyChild, To: RepairStage},
		{From: RepairStage, To: VerifyStage},
		{From: SelfReviewStage, To: CommitStage, When: "size(state.findings.findings) == 0"},
		{From: SelfReviewStage, To: RepairStage},
	}
	if len(def.Edges) != len(want) {
		return fmt.Errorf("codegen ir: flow %q has %d edges, want %d", def.Name, len(def.Edges), len(want))
	}
	for i, edge := range want {
		got := def.Edges[i]
		if got.From != edge.From || got.To != edge.To || got.When != edge.When {
			return fmt.Errorf("codegen ir: flow %q edge %d is %q -> %q when %q, want %q -> %q when %q",
				def.Name, i, got.From, got.To, got.When, edge.From, edge.To, edge.When)
		}
	}
	for _, stage := range ExpectedStages() {
		if _, ok := def.Nodes[stage]; !ok {
			return fmt.Errorf("codegen ir: flow %q has no stage %q", def.Name, stage)
		}
	}
	repair, review := LoopGuards()
	guards := []struct {
		stage string
		want  flow.Guard
	}{{RepairStage, repair}, {SelfReviewStage, review}}
	for _, guard := range guards {
		node := def.Nodes[guard.stage]
		if node.Guard == nil || *node.Guard != guard.want {
			return fmt.Errorf("codegen ir: flow %q stage %q guard %v, want %v", def.Name, guard.stage, node.Guard, guard.want)
		}
	}
	return nil
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
