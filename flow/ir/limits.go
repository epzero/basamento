package ir

import (
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/flow"
)

// The compiler's fixed limits (specs/flow-engine/design.md §2.2, §2.3). They
// are requirements, not tuning knobs: changing a value is a spec change
// (REQ-FLOW-045, REQ-FLOW-046). They apply to an IR built in Go as well as to
// one decoded from JSON or YAML.
const (
	// MaxNodes is the greatest number of nodes a definition may declare.
	MaxNodes = 4096
	// MaxEdges is the greatest number of edges a definition may declare.
	MaxEdges = 16384
	// MaxExpressionBytes is the greatest UTF-8 length of one expression: an
	// edge condition, a node input mapping, a guard condition or a spec
	// expression.
	MaxExpressionBytes = 16384
	// MaxDefinitionBytes is the greatest canonical JSON size of a definition,
	// because those bytes are what is hashed, pinned and stored.
	MaxDefinitionBytes = 1048576

	// MaxGuardVisits is the greatest guard max_visits.
	MaxGuardVisits = 1000000
	// MaxGuardCostMpts is the greatest guard cost_mpts, in milli-points.
	MaxGuardCostMpts = 1000000000
)

// SpecExpressionFields names the spec fields a node kind keeps a CEL or jq
// expression in (design.md §2.3): a gate's accept_if and a map's over. Every
// other spec field belongs to the node kind, not the compiler, so it is
// neither type-checked against the state schema nor measured against the
// expression size limit.
func SpecExpressionFields(kind string) []string {
	switch kind {
	case "gate":
		return []string{"accept_if"}
	case "map":
		return []string{"over"}
	}
	return nil
}

// CheckCanonicalSize rejects a canonical definition above MaxDefinitionBytes
// (REQ-FLOW-046). flow/compile calls it on the canonical bytes it is about to
// hash and pin, after they are produced once and before any expression is
// type-checked (design.md §2.3).
func CheckCanonicalSize(canonical []byte) error {
	if len(canonical) <= MaxDefinitionBytes {
		return nil
	}
	return invalid(fmt.Sprintf("flow: definition size limit of %d bytes exceeded: %d bytes", MaxDefinitionBytes, len(canonical)),
		map[string]any{"limit": "definition size", "maximum": MaxDefinitionBytes})
}

// validateCounts rejects a definition with too many nodes or edges
// (REQ-FLOW-046). It is linear and runs before any graph work, so an
// over-limit definition never pays for reachability or the cycle search.
func validateCounts(f *flow.IR) error {
	if len(f.Nodes) > MaxNodes {
		return invalid(fmt.Sprintf("flow: node count limit of %d nodes exceeded: %d nodes", MaxNodes, len(f.Nodes)),
			map[string]any{"limit": "node count", "maximum": MaxNodes})
	}
	if len(f.Edges) > MaxEdges {
		return invalid(fmt.Sprintf("flow: edge count limit of %d edges exceeded: %d edges", MaxEdges, len(f.Edges)),
			map[string]any{"limit": "edge count", "maximum": MaxEdges})
	}
	return nil
}

// validateGuards keeps every guard budget inside the range design.md §2.2
// gives (REQ-FLOW-045). Zero means the field is absent; a negative budget is
// contradictory and rejected wherever the guard appears, on a cycle or not.
// Nodes are visited in id order, so a definition with two bad guards always
// reports the same one.
func validateGuards(f *flow.IR) error {
	for _, id := range sortedKeys(f.Nodes) {
		guard := f.Nodes[id].Guard
		if guard == nil {
			continue
		}
		if err := checkGuardBudget(id, "max_visits", int64(guard.MaxVisits), MaxGuardVisits); err != nil {
			return err
		}
		if err := checkGuardBudget(id, "cost_mpts", guard.CostMpts, MaxGuardCostMpts); err != nil {
			return err
		}
	}
	return nil
}

func checkGuardBudget(node, field string, value, maximum int64) error {
	details := map[string]any{"node": node, "field": field, "maximum": maximum}
	switch {
	case value < 0:
		return invalid(fmt.Sprintf("flow: node %q: guard %s %d is negative; the maximum is %d", node, field, value, maximum), details)
	case value > maximum:
		return invalid(fmt.Sprintf("flow: node %q: guard %s %d is above the maximum %d", node, field, value, maximum), details)
	}
	return nil
}

// validateExpressionSizes rejects one expression above MaxExpressionBytes
// (REQ-FLOW-046). Nodes are visited in id order and edges in declaration
// order, so a definition with two oversized expressions always reports the
// same one.
func validateExpressionSizes(f *flow.IR) error {
	for _, id := range sortedKeys(f.Nodes) {
		node := f.Nodes[id]
		if err := checkExpressionSize(fmt.Sprintf("node %q input mapping", id), node.In); err != nil {
			return err
		}
		if node.Guard != nil {
			if err := checkExpressionSize(fmt.Sprintf("node %q guard while", id), node.Guard.While); err != nil {
				return err
			}
		}
		spec := specValues(node.Spec)
		for _, field := range SpecExpressionFields(node.Kind) {
			var value string
			if raw, ok := spec[field]; ok && json.Unmarshal(raw, &value) == nil {
				if err := checkExpressionSize(fmt.Sprintf("node %q %s %s", id, node.Kind, field), value); err != nil {
					return err
				}
			}
		}
	}
	for _, edge := range f.Edges {
		if err := checkExpressionSize(fmt.Sprintf("edge %q -> %q when", edge.From, edge.To), edge.When); err != nil {
			return err
		}
	}
	return nil
}

func checkExpressionSize(subject, expression string) error {
	if len(expression) <= MaxExpressionBytes {
		return nil
	}
	return invalid(fmt.Sprintf("flow: expression size limit of %d bytes exceeded: %s is %d bytes", MaxExpressionBytes, subject, len(expression)),
		map[string]any{"limit": "expression size", "maximum": MaxExpressionBytes, "subject": subject})
}

// specValues reads a node spec as an object for the size check. A spec the
// compiler rejects for its shape carries no expression to measure; REQ-FLOW-004
// reports it in flow/compile.
func specValues(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var spec map[string]json.RawMessage
	if json.Unmarshal(raw, &spec) != nil {
		return nil
	}
	return spec
}
