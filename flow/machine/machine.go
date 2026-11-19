package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

// Machine is the pure interpreter over one definition. The zero value is not
// usable; call New.
type Machine struct {
	def       *flow.IR
	maxFanout int
	edges     map[string][]compiledEdge
	guards    map[string]*expr.Program
	inputs    map[string]*valueExpr
	sets      map[string]*valueExpr
	mapOver   map[string]*valueExpr
	mapNodes  map[string]string
}

type compiledEdge struct {
	to   string
	cond *expr.Program
}

// valueExpr is a node mapping or a set expression: CEL when it compiles as
// CEL, otherwise a jq mapping over the state, the same rule the compiler uses
// (flow/compile).
type valueExpr struct {
	cel *expr.Program
	jq  *expr.Mapping
}

// New compiles the expressions of def and returns a Machine that runs it. The
// definition must pass flow/ir validation; an expression that does not
// compile is rejected with code invalid_argument, naming the node or edge.
func New(def *flow.IR, opts ...Option) (*Machine, error) {
	if def == nil {
		return nil, invalid("flow: nil definition", nil)
	}
	if err := ir.Validate(def); err != nil {
		return nil, err
	}
	env, err := expr.NewEnv([]expr.Var{
		{Name: "state", Schema: def.State.Schema},
		// input and run are dyn, as in the compiler: a node input and the run
		// budget take the shape of each node kind (docs/architecture/flow-engine.md §3).
		{Name: "input", Schema: json.RawMessage(`{}`)},
		{Name: "run", Schema: json.RawMessage(`{}`)},
	})
	if err != nil {
		return nil, invalid("flow: expressions: "+err.Error(), nil)
	}
	m := &Machine{
		def:       def,
		maxFanout: DefaultMaxFanout,
		edges:     make(map[string][]compiledEdge, len(def.Nodes)),
		guards:    make(map[string]*expr.Program),
		inputs:    make(map[string]*valueExpr),
		sets:      make(map[string]*valueExpr),
		mapOver:   make(map[string]*valueExpr),
		mapNodes:  make(map[string]string),
	}
	for _, opt := range opts {
		if err := opt(m); err != nil {
			return nil, err
		}
	}
	for _, edge := range def.Edges {
		compiled := compiledEdge{to: edge.To}
		if edge.When != "" {
			condition, err := env.Compile(edge.When, json.RawMessage(`{"type":"boolean"}`))
			if err != nil {
				return nil, expressionError(fmt.Sprintf("edge %q -> %q", edge.From, edge.To), err)
			}
			compiled.cond = condition
		}
		m.edges[edge.From] = append(m.edges[edge.From], compiled)
	}
	for _, id := range sortedNodeIDs(def) {
		node := def.Nodes[id]
		if node.In != "" {
			value, err := compileValue(env, node.In)
			if err != nil {
				return nil, expressionError(fmt.Sprintf("node %q: input mapping", id), err)
			}
			m.inputs[id] = value
		}
		if node.Guard != nil && node.Guard.While != "" {
			condition, err := env.Compile(node.Guard.While, json.RawMessage(`{"type":"boolean"}`))
			if err != nil {
				return nil, expressionError(fmt.Sprintf("node %q guard", id), err)
			}
			m.guards[id] = condition
		}
		switch node.Kind {
		case "set":
			value, err := compileSet(env, node)
			if err != nil {
				return nil, expressionError(fmt.Sprintf("node %q: set expression", id), err)
			}
			if value != nil {
				m.sets[id] = value
			}
		case "map":
			over, child, err := parseMapSpec(node)
			if err != nil {
				return nil, invalid(fmt.Sprintf("node %q: %s", id, err.Error()), map[string]any{"node": id})
			}
			value, err := compileValue(env, over)
			if err != nil {
				return nil, expressionError(fmt.Sprintf("node %q: map over", id), err)
			}
			m.mapOver[id] = value
			m.mapNodes[id] = child
		}
	}
	return m, nil
}

// Start begins a run with input and returns the first superstep's commands.
// The run's state is input, with the definition's defaults for keys input does
// not set. A run whose entry edge reaches FAIL completes at once. On error the
// returned snapshot is zero.
func (m *Machine) Start(input json.RawMessage) (flow.Snapshot, []flow.Command, error) {
	state, err := m.initialState(input)
	if err != nil {
		return flow.Snapshot{}, nil, err
	}
	s := flow.Snapshot{
		Seq:   1,
		State: state,
		Input: append(json.RawMessage(nil), input...),
		Visit: map[string]int{},
	}
	targets := m.entryTargets()
	if len(targets) == 0 {
		return s, []flow.Command{{Type: "complete"}}, nil
	}
	commands, err := m.advance(&s, targets)
	if err != nil {
		return flow.Snapshot{}, nil, err
	}
	return s, commands, nil
}

// Apply records one event against the current superstep. It returns no
// commands until every node of the superstep has reported; then it merges the
// updates, routes and starts the next superstep. The snapshot argument is never
// modified; on error the caller's snapshot is unchanged and the returned
// snapshot is zero.
func (m *Machine) Apply(s flow.Snapshot, ev flow.Event) (flow.Snapshot, []flow.Command, error) {
	work := copySnapshot(s)
	if work.Visit == nil {
		work.Visit = map[string]int{}
	}
	switch ev.Type {
	case "node_failed", "cancelled", "node_completed", "gate_resolved", "timer_fired":
	default:
		return flow.Snapshot{}, nil, invalid(fmt.Sprintf("flow: unknown event type %q", ev.Type), map[string]any{"type": ev.Type})
	}
	if err := validateCostMpts(work.Cost, ev); err != nil {
		return flow.Snapshot{}, nil, err
	}
	switch ev.Type {
	case "node_failed":
		accrueCostMpts(&work, ev)
		return work, []flow.Command{{Type: "fail", Node: ev.Node, Input: ev.Payload}}, nil
	case "cancelled":
		return work, []flow.Command{failure(ev.Node, core.CodeCancelled, "run cancelled")}, nil
	}
	index := -1
	for i := range work.Steps {
		step := &work.Steps[i]
		if step.Node == ev.Node && step.Attempt == ev.Attempt && !step.Done {
			index = i
			break
		}
	}
	if index < 0 {
		return flow.Snapshot{}, nil, invalid(fmt.Sprintf("flow: node %q has no pending step", ev.Node), map[string]any{"node": ev.Node})
	}
	if ev.Type == "node_completed" {
		accrueCostMpts(&work, ev)
	}
	step := &work.Steps[index]
	step.Done = true
	switch ev.Type {
	case "node_completed", "gate_resolved":
		step.Update = append(json.RawMessage(nil), ev.Payload...)
	}
	for i := range work.Steps {
		if !work.Steps[i].Done {
			return work, nil, nil
		}
	}
	next, failing, err := m.barrier(&work)
	if err != nil {
		return flow.Snapshot{}, nil, err
	}
	if failing != nil {
		return work, []flow.Command{*failing}, nil
	}
	if len(next) == 0 {
		return work, []flow.Command{{Type: "complete"}}, nil
	}
	work.Seq++
	commands, err := m.advance(&work, next)
	if err != nil {
		return flow.Snapshot{}, nil, err
	}
	return work, commands, nil
}

func validateCostMpts(current int64, ev flow.Event) error {
	if ev.CostMpts == 0 {
		return nil
	}
	switch ev.Type {
	case "node_completed", "node_failed":
	default:
		return invalid(fmt.Sprintf("flow: event type %q cannot carry cost_mpts", ev.Type), map[string]any{"type": ev.Type})
	}
	if ev.CostMpts < 0 {
		return invalid("flow: cost_mpts must not be negative", nil)
	}
	if ev.CostMpts > math.MaxInt64-current {
		return invalid("flow: cost_mpts overflow", nil)
	}
	return nil
}

func accrueCostMpts(s *flow.Snapshot, ev flow.Event) {
	if ev.CostMpts == 0 {
		return
	}
	s.Cost += ev.CostMpts
}

// advance expands a set of ready nodes and runs pure supersteps until it has
// commands to hand out or the run ends.
func (m *Machine) advance(s *flow.Snapshot, targets []string) ([]flow.Command, error) {
	for {
		steps, failing, err := m.expand(s, targets)
		if err != nil {
			return nil, err
		}
		if failing != nil {
			return []flow.Command{*failing}, nil
		}
		s.Steps = steps
		if len(steps) == 0 {
			return []flow.Command{{Type: "complete"}}, nil
		}
		if commands := m.commands(steps); len(commands) > 0 {
			return commands, nil
		}
		next, failing, err := m.barrier(s)
		if err != nil {
			return nil, err
		}
		if failing != nil {
			return []flow.Command{*failing}, nil
		}
		if len(next) == 0 {
			return []flow.Command{{Type: "complete"}}, nil
		}
		s.Seq++
		targets = next
	}
}

func (m *Machine) expand(s *flow.Snapshot, targets []string) ([]flow.Step, *flow.Command, error) {
	queue := normalise(targets)
	seen := make(map[string]bool, len(queue))
	var steps []flow.Step
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		if id == ir.Fail {
			return nil, failEndpoint(""), nil
		}
		node, ok := m.def.Nodes[id]
		if !ok {
			return nil, nil, invalid(fmt.Sprintf("flow: node %q does not exist", id), map[string]any{"node": id})
		}
		skip, stopped, err := m.checkGuard(s, id, node)
		if err != nil {
			return nil, nil, err
		}
		if stopped {
			return nil, guardFailure(id, node), nil
		}
		if skip {
			steps = append(steps, flow.Step{Node: id, Done: true, Skip: true})
			continue
		}
		s.Visit[id]++
		switch node.Kind {
		case "parallel":
			queue = append(queue, m.fanout(id)...)
		case "map":
			items, err := m.mapItems(s, id)
			if err != nil {
				return nil, nil, err
			}
			if len(items) > m.maxFanout {
				return nil, fanoutFailure(id, len(items), m.maxFanout), nil
			}
			for i, item := range items {
				steps = append(steps, flow.Step{Node: m.mapNodes[id], Attempt: i + 1, Input: item})
			}
		case "set":
			update, err := m.setUpdate(s, id, node)
			if err != nil {
				return nil, nil, err
			}
			steps = append(steps, flow.Step{Node: id, Done: true, Update: update})
		default:
			input, err := m.nodeInput(s, id, node)
			if err != nil {
				return nil, nil, err
			}
			steps = append(steps, flow.Step{Node: id, Input: input})
		}
	}
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Node != steps[j].Node {
			return steps[i].Node < steps[j].Node
		}
		return steps[i].Attempt < steps[j].Attempt
	})
	return steps, nil, nil
}

func (m *Machine) commands(steps []flow.Step) []flow.Command {
	var commands []flow.Command
	for _, step := range steps {
		if step.Done {
			continue
		}
		command := flow.Command{Type: "run_node", Node: step.Node, Attempt: step.Attempt, Input: step.Input}
		switch m.def.Nodes[step.Node].Kind {
		case "gate":
			command.Type = "await_gate"
		case "wait":
			command.Type = "start_timer"
		}
		commands = append(commands, command)
	}
	return commands
}

// barrier merges the superstep's updates and routes every node that ran. It
// returns the next superstep's targets, or the command that ends the run.
func (m *Machine) barrier(s *flow.Snapshot) ([]string, *flow.Command, error) {
	decoded, err := decodeJSON(s.State)
	if err != nil {
		return nil, nil, invalid("flow: state: "+err.Error(), nil)
	}
	state, ok := decoded.(map[string]any)
	if !ok {
		return nil, nil, invalid("flow: state is not an object", nil)
	}
	steps := append([]flow.Step(nil), s.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Node != steps[j].Node {
			return steps[i].Node < steps[j].Node
		}
		return steps[i].Attempt < steps[j].Attempt
	})
	routed := map[string]bool{}
	var targets []string
	for _, step := range steps {
		if step.Skip {
			continue
		}
		node := m.def.Nodes[step.Node]
		if err := m.mergeStep(state, node, step.Update); err != nil {
			return nil, nil, err
		}
		if routed[step.Node] {
			continue
		}
		routed[step.Node] = true
		next, failing, err := m.route(state, step.Node, s)
		if err != nil {
			return nil, nil, err
		}
		if failing != nil {
			return nil, failing, nil
		}
		targets = append(targets, next...)
	}
	encoded, err := encodeJSON(state)
	if err != nil {
		return nil, nil, invalid("flow: state: "+err.Error(), nil)
	}
	s.State = encoded
	return normalise(targets), nil, nil
}

// route follows the first outgoing edge whose condition holds, in declaration
// order, and otherwise the first edge without a condition (REQ-FLOW-001).
func (m *Machine) route(state map[string]any, from string, s *flow.Snapshot) ([]string, *flow.Command, error) {
	fallback, hasFallback := "", false
	for _, edge := range m.edges[from] {
		if edge.cond == nil {
			if !hasFallback {
				fallback, hasFallback = edge.to, true
			}
			continue
		}
		holds, err := m.evalBool(edge.cond, fmt.Sprintf("edge %q -> %q", from, edge.to), state, s)
		if err != nil {
			return nil, nil, err
		}
		if holds {
			targets, failing := m.edgeTarget(from, edge.to)
			return targets, failing, nil
		}
	}
	if hasFallback {
		targets, failing := m.edgeTarget(from, fallback)
		return targets, failing, nil
	}
	return nil, nil, nil
}

func (m *Machine) edgeTarget(from, to string) ([]string, *flow.Command) {
	if to == ir.Fail {
		return nil, failEndpoint(from)
	}
	return []string{to}, nil
}

func (m *Machine) checkGuard(s *flow.Snapshot, id string, node flow.Node) (skip, stopped bool, err error) {
	guard := node.Guard
	if guard == nil {
		return false, false, nil
	}
	if condition := m.guards[id]; condition != nil {
		state, err := decodedState(s)
		if err != nil {
			return false, false, err
		}
		holds, err := m.evalBool(condition, fmt.Sprintf("node %q guard", id), state, s)
		if err != nil {
			return false, false, err
		}
		if !holds {
			return true, false, nil
		}
	}
	if guard.MaxVisits > 0 && s.Visit[id]+1 > guard.MaxVisits {
		return false, true, nil
	}
	if guard.CostMpts > 0 && s.Cost >= guard.CostMpts {
		return false, true, nil
	}
	return false, false, nil
}

func (m *Machine) fanout(id string) []string {
	seen := map[string]bool{}
	var out []string
	for _, edge := range m.edges[id] {
		if !seen[edge.to] {
			seen[edge.to] = true
			out = append(out, edge.to)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Machine) mapItems(s *flow.Snapshot, id string) ([]json.RawMessage, error) {
	state, err := decodedState(s)
	if err != nil {
		return nil, err
	}
	value, err := m.mapOver[id].eval(state, decodedInput(s), runVars(s))
	if err != nil {
		return nil, expressionError(fmt.Sprintf("node %q: map over", id), err)
	}
	list, ok := value.([]any)
	if !ok {
		return nil, invalid(fmt.Sprintf("flow: node %q: map over is not an array", id), map[string]any{"node": id})
	}
	items := make([]json.RawMessage, len(list))
	for i, item := range list {
		encoded, err := encodeJSON(item)
		if err != nil {
			return nil, invalid(fmt.Sprintf("flow: node %q: map item %d: %s", id, i, err.Error()), map[string]any{"node": id})
		}
		items[i] = encoded
	}
	return items, nil
}

func (m *Machine) nodeInput(s *flow.Snapshot, id string, node flow.Node) (json.RawMessage, error) {
	if node.In == "" {
		return json.RawMessage(`{}`), nil
	}
	state, err := decodedState(s)
	if err != nil {
		return nil, err
	}
	value, err := m.inputs[id].eval(state, decodedInput(s), runVars(s))
	if err != nil {
		return nil, expressionError(fmt.Sprintf("node %q: input mapping", id), err)
	}
	encoded, err := encodeJSON(value)
	if err != nil {
		return nil, invalid(fmt.Sprintf("flow: node %q: input mapping: %s", id, err.Error()), map[string]any{"node": id})
	}
	return encoded, nil
}

// setUpdate evaluates a set node. A spec string is a CEL expression assigned
// to Out; a spec object is a map of literal state assignments.
func (m *Machine) setUpdate(s *flow.Snapshot, id string, node flow.Node) (json.RawMessage, error) {
	spec := bytes.TrimSpace(node.Spec)
	if len(spec) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if spec[0] == '"' {
		if node.Out == "" {
			return nil, invalid(fmt.Sprintf("flow: set node %q has an expression and no out key", id), map[string]any{"node": id})
		}
		state, err := decodedState(s)
		if err != nil {
			return nil, err
		}
		value, err := m.sets[id].eval(state, decodedInput(s), runVars(s))
		if err != nil {
			return nil, expressionError(fmt.Sprintf("node %q: set expression", id), err)
		}
		return encodeJSON(map[string]any{node.Out: value})
	}
	var assignments map[string]json.RawMessage
	if err := json.Unmarshal(spec, &assignments); err != nil {
		return nil, invalid(fmt.Sprintf("flow: set node %q: spec is neither an expression nor an object", id), map[string]any{"node": id})
	}
	out := make(map[string]any, len(assignments))
	for _, key := range sortedKeys(assignments) {
		value, err := decodeJSON(assignments[key])
		if err != nil {
			return nil, invalid(fmt.Sprintf("flow: set node %q: key %q: %s", id, key, err.Error()), map[string]any{"node": id})
		}
		out[key] = value
	}
	return encodeJSON(out)
}

func (m *Machine) evalBool(program *expr.Program, subject string, state map[string]any, s *flow.Snapshot) (bool, error) {
	value, err := program.Eval(map[string]any{
		"state": state,
		"input": decodedInput(s),
		"run":   runVars(s),
	})
	if err != nil {
		return false, expressionError(subject, err)
	}
	holds, ok := value.(bool)
	if !ok {
		return false, invalid("flow: condition did not evaluate to a boolean", nil)
	}
	return holds, nil
}

func (m *Machine) initialState(input json.RawMessage) (json.RawMessage, error) {
	state := map[string]any{}
	if len(bytes.TrimSpace(input)) > 0 {
		decoded, err := decodeJSON(input)
		if err != nil {
			return nil, invalid("flow: input: "+err.Error(), nil)
		}
		object, ok := decoded.(map[string]any)
		if !ok {
			return nil, invalid("flow: input must be a JSON object", nil)
		}
		state = object
	}
	for _, key := range sortedKeys(m.def.Defaults) {
		if _, ok := state[key]; ok {
			continue
		}
		value, err := decodeJSON(m.def.Defaults[key])
		if err != nil {
			return nil, invalid(fmt.Sprintf("flow: default %q: %s", key, err.Error()), nil)
		}
		state[key] = value
	}
	return encodeJSON(state)
}

func (m *Machine) entryTargets() []string {
	for _, edge := range m.def.Edges {
		if edge.From == ir.Start {
			return []string{edge.To}
		}
	}
	return nil
}

func compileValue(env *expr.Env, src string) (*valueExpr, error) {
	if program, err := env.Compile(src, nil); err == nil {
		return &valueExpr{cel: program}, nil
	}
	if mapping, err := expr.CompileMapping(src, []string{"state", "run"}); err == nil {
		return &valueExpr{jq: mapping}, nil
	}
	_, err := env.Compile(src, nil)
	return nil, err
}

func compileSet(env *expr.Env, node flow.Node) (*valueExpr, error) {
	spec := bytes.TrimSpace(node.Spec)
	if len(spec) == 0 || spec[0] != '"' {
		return nil, nil
	}
	var src string
	if err := json.Unmarshal(spec, &src); err != nil {
		return nil, err
	}
	return compileValue(env, src)
}

func parseMapSpec(node flow.Node) (over, child string, err error) {
	var spec struct {
		Over string `json:"over"`
		Node string `json:"node"`
		Max  int    `json:"max_parallel"` // read by the host; the machine fans out all items
	}
	if len(bytes.TrimSpace(node.Spec)) == 0 {
		return "", "", fmt.Errorf("map spec is empty")
	}
	if err := json.Unmarshal(node.Spec, &spec); err != nil {
		return "", "", fmt.Errorf("map spec: %s", err.Error())
	}
	if spec.Over == "" || spec.Node == "" {
		return "", "", fmt.Errorf("map spec needs over and node")
	}
	return spec.Over, spec.Node, nil
}

func (v *valueExpr) eval(state, input any, run map[string]any) (any, error) {
	if v.cel != nil {
		return v.cel.Eval(map[string]any{"state": state, "input": input, "run": run})
	}
	return v.jq.Eval(state, map[string]any{"state": state, "run": run})
}

func decodedState(s *flow.Snapshot) (map[string]any, error) {
	decoded, err := decodeJSON(s.State)
	if err != nil {
		return nil, invalid("flow: state: "+err.Error(), nil)
	}
	state, ok := decoded.(map[string]any)
	if !ok {
		return nil, invalid("flow: state is not an object", nil)
	}
	return state, nil
}

func decodedInput(s *flow.Snapshot) any {
	if len(s.Input) == 0 {
		return nil
	}
	value, err := decodeJSON(s.Input)
	if err != nil {
		return nil
	}
	return value
}

func runVars(s *flow.Snapshot) map[string]any {
	return map[string]any{"cost": s.Cost}
}

func failEndpoint(from string) *flow.Command {
	return &flow.Command{Type: "fail", Node: from}
}

func fanoutFailure(id string, count, limit int) *flow.Command {
	message := fmt.Sprintf("flow: node %q map over has %d items, limit %d", id, count, limit)
	payload, _ := json.Marshal(map[string]any{"code": string(core.CodeBudgetExceeded), "message": message})
	return &flow.Command{Type: "fail", Node: id, Input: payload}
}

func guardFailure(id string, node flow.Node) *flow.Command {
	message := fmt.Sprintf("flow: node %q reached its guard", id)
	if node.Guard != nil && node.Guard.MaxVisits > 0 {
		message = fmt.Sprintf("flow: node %q reached its guard of %d visits", id, node.Guard.MaxVisits)
	}
	if node.Guard != nil && node.Guard.CostMpts > 0 && node.Guard.MaxVisits == 0 {
		message = fmt.Sprintf("flow: node %q reached its guard of %d milli-points", id, node.Guard.CostMpts)
	}
	payload, _ := json.Marshal(map[string]any{"code": string(core.CodeBudgetExceeded), "message": message})
	return &flow.Command{Type: "fail", Node: id, Input: payload}
}

func failure(node string, code core.Code, message string) flow.Command {
	payload, _ := json.Marshal(map[string]any{"code": string(code), "message": message})
	return flow.Command{Type: "fail", Node: node, Input: payload}
}

func expressionError(subject string, err error) error {
	message := err.Error()
	details := map[string]any{"subject": subject}
	var platform *core.Error
	if errors.As(err, &platform) {
		message = platform.Message
		for key, value := range platform.Details {
			details[key] = value
		}
	}
	return invalid(subject+": "+message, details)
}

func normalise(targets []string) []string {
	if len(targets) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(targets))
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		if seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	sort.Strings(out)
	return out
}

func sortedNodeIDs(def *flow.IR) []string {
	ids := make([]string, 0, len(def.Nodes))
	for id := range def.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func decodeJSON(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err == nil {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return value, nil
}

func encodeJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func invalid(message string, details map[string]any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, message, details)
}
