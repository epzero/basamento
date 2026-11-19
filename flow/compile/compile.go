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

// Compile validates f, enforces the definition size limit and type-checks its
// expressions.
//
// The order is the one specs/flow-engine/design.md §2.3 pins: ir.Validate
// checks the counts, guard budgets and expression sizes first, the canonical
// form is produced once and rejected above 1 MiB before it is hashed or its
// expressions are type-checked, and only then does the type-checking run.
//
// On success the returned Compiled carries the same IR, its RFC 8785 canonical
// JSON and the SHA-256 hash of those bytes. Errors are *core.Error with code
// invalid_argument naming the node or edge and the expression at fault
// (REQ-FLOW-003, REQ-FLOW-004, REQ-FLOW-040 to REQ-FLOW-043, REQ-FLOW-045,
// REQ-FLOW-046).
func Compile(f *flow.IR) (*Compiled, error) {
	if err := ir.Validate(f); err != nil {
		return nil, err
	}
	canonical, err := ir.Canonical(f)
	if err != nil {
		return nil, err
	}
	if err := ir.CheckCanonicalSize(canonical); err != nil {
		return nil, err
	}
	if err := checkExpressions(f); err != nil {
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

// checkExpressions type-checks node inputs, node spec expressions, guard
// conditions and edge conditions in a deterministic order: nodes by id, then
// edges in declaration order. A mapping is CEL when it compiles as CEL,
// otherwise jq.
//
// The environment is built for every definition, with expressions or without:
// a state schema the expression layer cannot type-check must give one verdict
// whichever flow carries it, not pass because its flow happens to have no
// expression.
func checkExpressions(f *flow.IR) error {
	env, err := expressionEnv(f)
	if err != nil {
		return err
	}
	for _, id := range sortedNodeIDs(f) {
		if err := checkNode(env, id, f.Nodes[id]); err != nil {
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
		if err := checkMapping(env, fmt.Sprintf("node %q", id), "input mapping", node.In); err != nil {
			return err
		}
	}
	if node.Guard != nil && node.Guard.While != "" {
		if err := checkCondition(env, fmt.Sprintf("node %q guard", id), node.Guard.While); err != nil {
			return err
		}
	}
	for _, spec := range nodeSpecExpressions(node) {
		subject := fmt.Sprintf("node %q %s", id, spec.subject)
		if spec.condition {
			if err := checkCondition(env, subject, spec.value); err != nil {
				return err
			}
			continue
		}
		if err := checkMapping(env, subject, "", spec.value); err != nil {
			return err
		}
	}
	return nil
}

// specExpression is one CEL or jq expression a node kind keeps in its spec. A
// condition is type-checked as a boolean; a mapping is only checked as an
// expression, because its result shape belongs to the node kind.
type specExpression struct {
	subject   string // names the kind and spec field in errors
	value     string
	condition bool
}

// nodeSpecExpressions returns the spec expressions the compiler checks: a
// gate's accept_if and a map's over. A set node's assignments have no field in
// the IR yet (specs/flow-engine/design.md §2, open questions), so there is
// nothing to check for it.
func nodeSpecExpressions(node flow.Node) []specExpression {
	if len(node.Spec) == 0 {
		return nil
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(node.Spec, &spec); err != nil {
		return nil
	}
	switch node.Kind {
	case "gate":
		return specString(spec, "gate accept_if", "accept_if", true)
	case "map":
		return specString(spec, "map over", "over", false)
	}
	return nil
}

func specString(spec map[string]json.RawMessage, subject, field string, condition bool) []specExpression {
	raw, ok := spec[field]
	if !ok {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return nil
	}
	return []specExpression{{subject: subject, value: value, condition: condition}}
}

func checkMapping(env *expr.Env, subject, label, src string) error {
	_, celErr := env.Compile(src, nil)
	if celErr == nil {
		return nil
	}
	_, mappingErr := expr.CompileMapping(src, nil)
	if mappingErr == nil {
		return nil
	}
	return expressionError(joinSubject(subject, label), bothMappingErrors(celErr, mappingErr))
}

// bothMappingErrors keeps the CEL and the jq message: neither parser says
// alone why a text is neither.
func bothMappingErrors(celErr, mappingErr error) error {
	message := fmt.Sprintf("not CEL (%s) and not jq (%s)", messageOf(celErr), messageOf(mappingErr))
	details := map[string]any{}
	for _, err := range []error{celErr, mappingErr} {
		for key, value := range errorDetails(err) {
			details[key] = value
		}
	}
	return core.NewError(core.CodeInvalidArgument, message, details)
}

func messageOf(err error) string {
	var platform *core.Error
	if errors.As(err, &platform) {
		return platform.Message
	}
	return err.Error()
}

func errorDetails(err error) map[string]any {
	var platform *core.Error
	if errors.As(err, &platform) {
		return platform.Details
	}
	return nil
}

func joinSubject(subject, label string) string {
	if label == "" {
		return subject
	}
	return subject + ": " + label
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
