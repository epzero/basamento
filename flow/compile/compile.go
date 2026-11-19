package compile

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

// booleanSchema is the result schema every flow condition must satisfy.
const booleanSchema = `{"type":"boolean"}`

// Compiled is a flow definition that passed validation and type-checking.
// Its canonical bytes and hash are the definition identity a run pins.
type Compiled struct {
	definition *flow.IR
	canonical  []byte
	hash       string
}

// Compile validates f and type-checks its expressions.
//
// On success the returned Compiled carries the same IR, its RFC 8785 canonical
// JSON and the SHA-256 hash of those bytes. Errors are *core.Error with code
// invalid_argument naming the node or edge and the expression at fault
// (REQ-FLOW-003, REQ-FLOW-004, REQ-FLOW-040 to REQ-FLOW-043).
func Compile(f *flow.IR) (*Compiled, error) {
	if err := ir.Validate(f); err != nil {
		return nil, err
	}
	if err := checkExpressions(f); err != nil {
		return nil, err
	}
	canonical, err := ir.Canonical(f)
	if err != nil {
		return nil, err
	}
	return &Compiled{definition: f, canonical: canonical, hash: ir.HashCanonical(canonical)}, nil
}

// CompileJSON decodes a JSON or YAML flow definition and compiles it.
func CompileJSON(data []byte) (*Compiled, error) {
	f, err := ir.Decode(data)
	if err != nil {
		return nil, err
	}
	return Compile(f)
}

// IR returns the compiled definition. Callers must treat it as read-only.
func (c *Compiled) IR() *flow.IR { return c.definition }

// Canonical returns a copy of the canonical IR bytes.
func (c *Compiled) Canonical() []byte {
	out := make([]byte, len(c.canonical))
	copy(out, c.canonical)
	return out
}

// Hash returns the lowercase hex SHA-256 of the canonical IR.
func (c *Compiled) Hash() string { return c.hash }

// checkExpressions type-checks node inputs, guard conditions and edge
// conditions in a deterministic order: nodes by id, then edges in declaration
// order. A mapping is CEL when it compiles as CEL, otherwise jq.
func checkExpressions(f *flow.IR) error {
	if !hasExpressions(f) {
		return nil
	}
	env, err := expressionEnv(f)
	if err != nil {
		return err
	}
	for _, id := range sortedNodeIDs(f) {
		node := f.Nodes[id]
		if err := checkNode(env, id, node); err != nil {
			return err
		}
	}
	for _, edge := range f.Edges {
		if edge.When == "" {
			continue
		}
		subject := fmt.Sprintf("edge %q -> %q", edge.From, edge.To)
		if err := checkCondition(env, subject, edge.When); err != nil {
			return err
		}
	}
	return nil
}

func hasExpressions(f *flow.IR) bool {
	for _, node := range f.Nodes {
		if node.In != "" || (node.Guard != nil && node.Guard.While != "") {
			return true
		}
	}
	for _, edge := range f.Edges {
		if edge.When != "" {
			return true
		}
	}
	return false
}

func expressionEnv(f *flow.IR) (*expr.Env, error) {
	env, err := expr.NewEnv([]expr.Var{
		{Name: "state", Schema: f.State.Schema},
		// input and run are dyn: node inputs are shaped by each node kind and
		// run.cost is the budget the machine keeps (docs/architecture/flow-engine.md §3).
		{Name: "input", Schema: json.RawMessage(`{}`)},
		{Name: "run", Schema: json.RawMessage(`{}`)},
	})
	if err != nil {
		return nil, expressionError("flow: expressions", err)
	}
	return env, nil
}

func checkNode(env *expr.Env, id string, node flow.Node) error {
	if node.In != "" {
		if err := checkMapping(env, fmt.Sprintf("node %q", id), node.In); err != nil {
			return err
		}
	}
	if node.Guard != nil && node.Guard.While != "" {
		if err := checkCondition(env, fmt.Sprintf("node %q guard", id), node.Guard.While); err != nil {
			return err
		}
	}
	return nil
}

func checkMapping(env *expr.Env, subject, src string) error {
	_, celErr := env.Compile(src, nil)
	if celErr == nil {
		return nil
	}
	if _, mappingErr := expr.CompileMapping(src, nil); mappingErr == nil {
		return nil
	}
	return expressionError(subject+": input mapping", celErr)
}

func checkCondition(env *expr.Env, subject, src string) error {
	if _, err := env.Compile(src, json.RawMessage(booleanSchema)); err != nil {
		return expressionError(subject, err)
	}
	return nil
}

// expressionError adds the node or edge subject to expr's message, keeping
// the expression text and its details so callers can report both.
func expressionError(subject string, err error) error {
	var platform *core.Error
	message := err.Error()
	details := map[string]any{"subject": subject}
	if errors.As(err, &platform) {
		message = platform.Message
		for key, value := range platform.Details {
			details[key] = value
		}
	}
	return core.NewError(core.CodeInvalidArgument, subject+": "+message, details)
}

func sortedNodeIDs(f *flow.IR) []string {
	ids := make([]string, 0, len(f.Nodes))
	for id := range f.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
