package ir

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/flow"
)

// nodeKinds are the kinds the machine and host know (docs/architecture/flow-engine.md §1).
var nodeKinds = map[string]bool{
	"set":      true,
	"task":     true,
	"model":    true,
	"agent":    true,
	"tool":     true,
	"map":      true,
	"parallel": true,
	"subflow":  true,
	"gate":     true,
	"wait":     true,
}

// Validate checks everything the compiler can know from the definition alone:
// the state schema and its reducers, node names, kinds and reserved names, edge
// endpoints, exactly one edge from START, reachability and guarded cycles.
// Expression typing is flow/compile's job.
func Validate(f *flow.IR) error {
	if f == nil {
		return invalid("flow: nil definition", nil)
	}
	if f.Name == "" {
		return invalid("flow: name is required", nil)
	}
	if err := validateState(f); err != nil {
		return err
	}
	if err := validateNodes(f); err != nil {
		return err
	}
	if err := validateEdges(f); err != nil {
		return err
	}
	if err := validateReachable(f); err != nil {
		return err
	}
	return validateCycles(f)
}

func validateState(f *flow.IR) error {
	if _, err := schema.Compile(f.State.Schema); err != nil {
		return withLocation(err, "flow: state schema: ")
	}
	properties := map[string]bool{}
	if len(f.State.Schema) > 0 {
		var doc map[string]any
		if err := json.Unmarshal(f.State.Schema, &doc); err == nil {
			if props, ok := doc["properties"].(map[string]any); ok {
				for key := range props {
					properties[key] = true
				}
			}
		}
	}
	for _, key := range sortedKeys(f.State.Reducers) {
		if !properties[key] {
			return invalid(fmt.Sprintf("flow: state reducer %q: key is not in the state schema", key), map[string]any{"reducer": key})
		}
		if !validReducer(f.State.Reducers[key]) {
			return invalid(fmt.Sprintf("flow: state reducer %q: unknown reducer %q", key, f.State.Reducers[key]), map[string]any{"reducer": key})
		}
	}
	return nil
}

// validReducer accepts the reducer forms declared by ADR-0003: replace, append,
// merge, sum, and merge_by(<field>).
func validReducer(reducer string) bool {
	switch reducer {
	case "replace", "append", "merge", "sum":
		return true
	}
	const prefix = "merge_by("
	if strings.HasPrefix(reducer, prefix) && strings.HasSuffix(reducer, ")") {
		field := reducer[len(prefix) : len(reducer)-1]
		return field != "" && !strings.ContainsAny(field, "()")
	}
	return false
}

func validateNodes(f *flow.IR) error {
	for _, id := range sortedKeys(f.Nodes) {
		if id == "" {
			return invalid("flow: node id is required", nil)
		}
		if id == Start || id == Fail {
			return invalid(fmt.Sprintf("flow: node %q is a reserved edge endpoint, not a node", id), map[string]any{"node": id})
		}
		node := f.Nodes[id]
		if !nodeKinds[node.Kind] {
			return invalid(fmt.Sprintf("flow: node %q: unknown kind %q", id, node.Kind), map[string]any{"node": id, "kind": node.Kind})
		}
	}
	return nil
}

func validateEdges(f *flow.IR) error {
	startEdges := 0
	for _, edge := range f.Edges {
		label := fmt.Sprintf("flow: edge %q -> %q", edge.From, edge.To)
		details := map[string]any{"from": edge.From, "to": edge.To}
		switch {
		case edge.From == Fail:
			return invalid(label+": FAIL is only an edge target", details)
		case edge.To == Start:
			return invalid(label+": START is only an edge source", details)
		case edge.From != Start && !nodeExists(f, edge.From):
			return invalid(fmt.Sprintf("%s: unknown source node %q", label, edge.From), details)
		case edge.To != Fail && !nodeExists(f, edge.To):
			return invalid(fmt.Sprintf("%s: unknown target node %q", label, edge.To), details)
		}
		if edge.From == Start {
			startEdges++
		}
	}
	if startEdges != 1 {
		return invalid(fmt.Sprintf("flow: flow needs exactly one edge from %q, got %d", Start, startEdges), map[string]any{"edges": startEdges})
	}
	return nil
}

func validateReachable(f *flow.IR) error {
	reached := map[string]bool{Start: true}
	queue := []string{Start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range f.Edges {
			if edge.From != current || edge.To == Fail || reached[edge.To] {
				continue
			}
			reached[edge.To] = true
			queue = append(queue, edge.To)
		}
	}
	for _, id := range sortedKeys(f.Nodes) {
		if !reached[id] {
			return invalid(fmt.Sprintf("flow: node %q is unreachable from %s", id, Start), map[string]any{"node": id})
		}
	}
	return nil
}

// validateCycles requires at least one guarded node on every cycle
// (REQ-FLOW-003). An empty guard bounds nothing, so it does not count.
func validateCycles(f *flow.IR) error {
	adjacency := make(map[string][]string, len(f.Nodes))
	for id := range f.Nodes {
		adjacency[id] = nil
	}
	for _, edge := range f.Edges {
		if _, ok := adjacency[edge.From]; !ok {
			continue
		}
		if _, ok := adjacency[edge.To]; !ok {
			continue
		}
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	for _, component := range stronglyConnected(adjacency) {
		if !isCycle(component, adjacency) || hasGuardedNode(f, component) {
			continue
		}
		// The smallest node id makes the message independent of map order.
		sort.Strings(component)
		id := component[0]
		return invalid(fmt.Sprintf("flow: node %q is on a cycle with no guarded node", id), map[string]any{"node": id})
	}
	return nil
}

func isCycle(component []string, adjacency map[string][]string) bool {
	if len(component) > 1 {
		return true
	}
	id := component[0]
	for _, next := range adjacency[id] {
		if next == id {
			return true
		}
	}
	return false
}

func hasGuardedNode(f *flow.IR, component []string) bool {
	for _, id := range component {
		guard := f.Nodes[id].Guard
		if guard != nil && (guard.MaxVisits > 0 || guard.CostMpts > 0 || guard.While != "") {
			return true
		}
	}
	return false
}

// stronglyConnected returns the strongly connected components (Tarjan), so a
// cycle with another path out and back is still one component.
func stronglyConnected(adjacency map[string][]string) [][]string {
	ids := make([]string, 0, len(adjacency))
	for id := range adjacency {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state := &tarjanState{
		indices: map[string]int{},
		low:     map[string]int{},
		onStack: map[string]bool{},
		adj:     adjacency,
	}
	for _, id := range ids {
		if _, seen := state.indices[id]; !seen {
			state.visit(id)
		}
	}
	return state.components
}

type tarjanState struct {
	next       int
	indices    map[string]int
	low        map[string]int
	onStack    map[string]bool
	stack      []string
	adj        map[string][]string
	components [][]string
}

func (s *tarjanState) visit(id string) {
	s.indices[id] = s.next
	s.low[id] = s.next
	s.next++
	s.stack = append(s.stack, id)
	s.onStack[id] = true

	for _, next := range s.adj[id] {
		if _, seen := s.indices[next]; !seen {
			s.visit(next)
			if s.low[next] < s.low[id] {
				s.low[id] = s.low[next]
			}
		} else if s.onStack[next] && s.indices[next] < s.low[id] {
			s.low[id] = s.indices[next]
		}
	}

	if s.low[id] != s.indices[id] {
		return
	}
	var component []string
	for {
		last := s.stack[len(s.stack)-1]
		s.stack = s.stack[:len(s.stack)-1]
		s.onStack[last] = false
		component = append(component, last)
		if last == id {
			break
		}
	}
	s.components = append(s.components, component)
}

func nodeExists(f *flow.IR, id string) bool {
	_, ok := f.Nodes[id]
	return ok
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
