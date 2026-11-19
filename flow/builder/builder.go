package builder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/flow"
)

// Start and Fail are the reserved endpoints of the IR's edge lists. Neither is
// declared as a node, and the builder writes them verbatim (REQ-FLOW-014).
const (
	Start = "START"
	Fail  = "FAIL"
)

// defaultStateSchema is what a flow declares when it names no state: an object
// with no properties, so reducers and outputs are rejected until keys exist.
var defaultStateSchema = json.RawMessage(`{"type":"object"}`)

// Kind is a node kind. The constants are the kinds the machine and host know;
// Build does not check them, because the shape of a spec and the kind set are
// the compiler's checks (specs/flow-engine/design.md §1).
type Kind string

const (
	KindSet      Kind = "set"
	KindTask     Kind = "task"
	KindModel    Kind = "model"
	KindAgent    Kind = "agent"
	KindTool     Kind = "tool"
	KindMap      Kind = "map"
	KindParallel Kind = "parallel"
	KindSubflow  Kind = "subflow"
	KindGate     Kind = "gate"
	KindWait     Kind = "wait"
)

// Builder collects flow declarations and builds their IR. Its methods append
// declarations and return the builder; Build reads them and never mutates it,
// so the same declarations build the same bytes every time (REQ-FLOW-011).
//
// A Builder is not safe for concurrent use.
type Builder struct {
	name      string
	version   int
	schema    json.RawMessage
	stateType reflect.Type
	reducers  []reducerDecl
	nodes     []nodeDecl
	edges     []flow.Edge
	entries   []string
}

type reducerDecl struct {
	key     string
	reducer string
}

type nodeDecl struct {
	name  string
	kind  string
	spec  json.RawMessage
	in    string
	out   string
	guard *flow.Guard
}

// New starts a builder for the flow with the given name and version.
func New(name string, version int) *Builder {
	return &Builder{name: name, version: version}
}

// StateSchema declares the flow's state as a JSON Schema, replacing any state
// declared before. Build checks that it is a valid JSON Schema (REQ-FLOW-026).
func (b *Builder) StateSchema(raw json.RawMessage) *Builder {
	if len(bytes.TrimSpace(raw)) == 0 {
		b.schema = nil
	} else {
		b.schema = clone(raw)
	}
	b.stateType = nil
	return b
}

// StateType declares the flow's state as a Go type and generates its JSON
// Schema with core/schema, replacing any state declared before (REQ-FLOW-021).
func (b *Builder) StateType(t reflect.Type) *Builder {
	b.stateType = t
	b.schema = nil
	return b
}

// Reducer declares the reducer of a state key: replace, append, merge, sum or
// merge_by(<field>). Build rejects a key the state schema does not declare
// (REQ-FLOW-024); the reducer form is the compiler's check.
func (b *Builder) Reducer(key, reducer string) *Builder {
	b.reducers = append(b.reducers, reducerDecl{key: key, reducer: reducer})
	return b
}

// Node declares a node with its name, kind and spec. The shape of the spec is
// the compiler's check; Build writes it into the IR as declared, in canonical
// JSON (REQ-FLOW-012).
func (b *Builder) Node(name string, kind Kind, spec json.RawMessage, opts ...NodeOption) *Builder {
	cfg := nodeConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	decl := nodeDecl{name: name, kind: string(kind), spec: clone(spec), in: cfg.in, out: cfg.out}
	if cfg.guard != nil {
		guard := *cfg.guard
		decl.guard = &guard
	}
	b.nodes = append(b.nodes, decl)
	return b
}

// NodeOption customizes a node declaration. In, Out and Guard are the options.
type NodeOption func(*nodeConfig)

type nodeConfig struct {
	in    string
	out   string
	guard *flow.Guard
}

// In declares the node's mapping from state to its input, kept verbatim for
// the compiler to type-check.
func In(expr string) NodeOption {
	return func(c *nodeConfig) { c.in = expr }
}

// Out declares the state key that receives the node's update. Build rejects a
// key the state schema does not declare (REQ-FLOW-019).
func Out(key string) NodeOption {
	return func(c *nodeConfig) { c.out = key }
}

// Guard declares the node's guard, which bounds a cycle through it.
func Guard(g flow.Guard) NodeOption {
	return func(c *nodeConfig) { c.guard = &g }
}

// Edge declares an edge from a node or Start to a node or Fail, with an
// optional CEL condition. Edges keep their declaration order, which is the
// routing order (REQ-FLOW-001, REQ-FLOW-013).
func (b *Builder) Edge(from, to, when string) *Builder {
	b.edges = append(b.edges, flow.Edge{From: from, To: to, When: when})
	return b
}

// Entry marks a node as the entry node, the same as an edge from Start with no
// condition. Build rejects more than one marked node (REQ-FLOW-014,
// REQ-FLOW-018).
func (b *Builder) Entry(node string) *Builder {
	b.entries = append(b.entries, node)
	return b.Edge(Start, node, "")
}

// Build returns the flow's IR. It checks, in order and returning the first
// failure: the state schema, reducer keys, duplicate node names, node outputs,
// edge endpoints, marked entry nodes and the presence of an entry. Every
// rejection is a *core.Error with code invalid_argument (REQ-FLOW-015 …
// REQ-FLOW-019, REQ-FLOW-024, REQ-FLOW-026).
//
// Build returns a new IR each call and reads no clock, randomness or instance
// identity (REQ-FLOW-010, REQ-FLOW-011).
func (b *Builder) Build() (*flow.IR, error) {
	stateSchema, err := b.stateSchema()
	if err != nil {
		return nil, b.schemaError(err)
	}
	if _, err := schema.Compile(stateSchema); err != nil {
		return nil, b.schemaError(err)
	}
	canonicalSchema, err := schema.Canonicalize(stateSchema)
	if err != nil {
		return nil, b.schemaError(err)
	}
	keys := schemaKeys(canonicalSchema)

	for _, reducer := range b.reducers {
		if !keys[reducer.key] {
			return nil, b.errorf(map[string]any{"reducer": reducer.key},
				"reducer for state key %q: key is not in the state schema", reducer.key)
		}
	}

	declared := make(map[string]bool, len(b.nodes))
	for _, node := range b.nodes {
		if declared[node.name] {
			return nil, b.errorf(map[string]any{"node": node.name}, "duplicate node %q", node.name)
		}
		declared[node.name] = true
	}

	nodes := make(map[string]flow.Node, len(b.nodes))
	for _, node := range b.nodes {
		if node.out != "" && !keys[node.out] {
			return nil, b.errorf(map[string]any{"node": node.name, "key": node.out},
				"node %q: output %q is not a state key", node.name, node.out)
		}
		spec := clone(node.spec)
		if len(bytes.TrimSpace(spec)) > 0 {
			canonical, err := schema.Canonicalize(spec)
			if err != nil {
				return nil, b.errorf(map[string]any{"node": node.name},
					"node %q: invalid node spec: %s", node.name, errorMessage(err))
			}
			spec = canonical
		}
		nodes[node.name] = flow.Node{
			Kind:  node.kind,
			In:    node.in,
			Out:   node.out,
			Spec:  spec,
			Guard: cloneGuard(node.guard),
		}
	}

	for _, edge := range b.edges {
		if edge.From != Start && !declared[edge.From] {
			return nil, b.edgeError(edge, edge.From)
		}
		if edge.To != Fail && !declared[edge.To] {
			return nil, b.edgeError(edge, edge.To)
		}
	}

	if entries := distinctNames(b.entries); len(entries) > 1 {
		return nil, b.errorf(map[string]any{"nodes": entries},
			"more than one entry node: %s", quoteNodes(entries))
	}

	entryEdges := 0
	for _, edge := range b.edges {
		if edge.From == Start {
			entryEdges++
		}
	}
	if entryEdges == 0 {
		return nil, b.errorf(nil,
			"flow has no entry: declare an edge from %q or mark a node with Entry", Start)
	}

	return &flow.IR{
		Name:    b.name,
		Version: b.version,
		State:   flow.StateSpec{Schema: canonicalSchema, Reducers: reducerMap(b.reducers)},
		Nodes:   nodes,
		Edges:   append([]flow.Edge(nil), b.edges...),
	}, nil
}

func (b *Builder) stateSchema() (json.RawMessage, error) {
	if b.stateType != nil {
		generated, err := schema.ForType(b.stateType)
		if err != nil {
			return nil, err
		}
		return clone(generated.Raw), nil
	}
	if len(bytes.TrimSpace(b.schema)) > 0 {
		return clone(b.schema), nil
	}
	return clone(defaultStateSchema), nil
}

func (b *Builder) errorf(details map[string]any, format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("flow %q: ", b.name)+fmt.Sprintf(format, args...), details)
}

func (b *Builder) edgeError(edge flow.Edge, missing string) *core.Error {
	return b.errorf(map[string]any{"from": edge.From, "to": edge.To, "node": missing},
		"edge %q -> %q: unknown node %q", edge.From, edge.To, missing)
}

// schemaError keeps the schema package's violation details and adds the first
// violation's location, so callers can report where the schema is wrong
// (REQ-FLOW-026).
func (b *Builder) schemaError(err error) *core.Error {
	message := errorMessage(err)
	details := map[string]any{}
	var platform *core.Error
	if errors.As(err, &platform) {
		for key, value := range platform.Details {
			details[key] = value
		}
	}
	if _, ok := details["location"]; !ok {
		if violations := schema.SchemaViolations(err); len(violations) > 0 {
			details["location"] = violations[0].SchemaLocation
		}
	}
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("flow %q: state schema: %s", b.name, message), details)
}

func schemaKeys(raw json.RawMessage) map[string]bool {
	keys := map[string]bool{}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return keys
	}
	for key := range doc.Properties {
		keys[key] = true
	}
	return keys
}

func reducerMap(decls []reducerDecl) map[string]string {
	if len(decls) == 0 {
		return nil
	}
	reducers := make(map[string]string, len(decls))
	for _, decl := range decls {
		reducers[decl.key] = decl.reducer
	}
	return reducers
}

// distinctNames keeps the first occurrence of each name, so marking one node
// twice is not reported as two marked entry nodes (REQ-FLOW-018).
func distinctNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func quoteNodes(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("node %q", name)
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

func clone(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneGuard(guard *flow.Guard) *flow.Guard {
	if guard == nil {
		return nil
	}
	copied := *guard
	return &copied
}

func errorMessage(err error) string {
	var platform *core.Error
	if errors.As(err, &platform) {
		return platform.Message
	}
	return err.Error()
}
