package ir

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

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
// the compiler's limits, the state schema and its reducers, node names, kinds
// and reserved names, edge endpoints, exactly one unconditional edge from
// START, map children the machine cannot run, reachability and guarded cycles.
// Expression typing is flow/compile's job.
//
// The limits come first, so an over-limit definition is rejected before any
// graph work and never reaches the cycle search (specs/flow-engine/design.md
// §2.3).
func Validate(f *flow.IR) error {
	if f == nil {
		return invalid("flow: nil definition", nil)
	}
	if err := validateCounts(f); err != nil {
		return err
	}
	if err := validateGuards(f); err != nil {
		return err
	}
	if err := validateExpressionSizes(f); err != nil {
		return err
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
	if err := validateMaps(f); err != nil {
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
	properties, everyObject := StateProperties(f.State.Schema)
	for _, key := range sortedKeys(f.State.Reducers) {
		if !everyObject && !properties[key] {
			return invalid(fmt.Sprintf("flow: state reducer %q: key is not in the state schema", key), map[string]any{"reducer": key})
		}
		if !validReducer(f.State.Reducers[key]) {
			return invalid(fmt.Sprintf("flow: state reducer %q: unknown reducer %q", key, f.State.Reducers[key]), map[string]any{"reducer": key})
		}
	}
	return nil
}

// StateProperties returns the property names the state schema declares and
// whether it admits every object. Local $ref and allOf composition are
// resolved, so a schema split across $defs is read the way core/schema reads
// it; a boolean true schema admits every object, so every reducer key is in it.
func StateProperties(raw json.RawMessage) (map[string]bool, bool) {
	var doc any
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
		return map[string]bool{}, false
	}
	walk := &propertyWalk{root: doc, seen: map[string]bool{}, properties: map[string]bool{}}
	walk.collect(doc)
	return walk.properties, walk.everyObject
}

type propertyWalk struct {
	root        any
	seen        map[string]bool // $refs already followed, so a cycle terminates
	properties  map[string]bool
	everyObject bool
}

func (w *propertyWalk) collect(node any) {
	switch n := node.(type) {
	case bool:
		w.everyObject = w.everyObject || n
	case map[string]any:
		// Draft 2020-12 applies $ref alongside its siblings, so both the
		// target and this node's own keywords are read.
		if ref, ok := n["$ref"].(string); ok && !w.seen[ref] {
			w.seen[ref] = true
			if target, ok := resolveLocalRef(w.root, ref); ok {
				w.collect(target)
			}
		}
		if props, ok := n["properties"].(map[string]any); ok {
			for key := range props {
				w.properties[key] = true
			}
		}
		if all, ok := n["allOf"].([]any); ok {
			for _, sub := range all {
				w.collect(sub)
			}
		}
	}
}

// resolveLocalRef follows a JSON Pointer into the same document. Refs outside
// it are rejected by core/schema's closed set before this runs.
func resolveLocalRef(root any, ref string) (any, bool) {
	if ref == "" || ref == "#" {
		return root, true
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	current := root
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
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
		// A node id reaches error messages and JSON Pointers, so a control
		// character in one would corrupt both.
		if strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return invalid(fmt.Sprintf("flow: node id %q contains a control character", id), map[string]any{"node": id})
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
	var startEdge flow.Edge
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
			startEdge = edge
		}
	}
	if startEdges != 1 {
		return invalid(fmt.Sprintf("flow: flow needs exactly one edge from %q, got %d", Start, startEdges), map[string]any{"edges": startEdges})
	}
	// START is where a run enters, before any node has routed, so the first
	// superstep has nothing to route on and the machine would ignore the
	// condition (REQ-FLOW-066).
	if startEdge.When != "" {
		return invalid(fmt.Sprintf("flow: edge %q -> %q takes no condition", startEdge.From, startEdge.To),
			map[string]any{"from": startEdge.From, "to": startEdge.To})
	}
	return nil
}

// validateMaps rejects the map definitions the machine cannot run once a run is
// in flight (REQ-FLOW-062 to REQ-FLOW-065). Nodes are scanned in id order, so
// two map nodes using one child always report them in the same order, and the
// check runs before the cycle search: a guarded map child is REQ-FLOW-065, not
// a cycle that looks bounded (design.md §2.6).
func validateMaps(f *flow.IR) error {
	owners := map[string]string{}
	for _, id := range sortedKeys(f.Nodes) {
		node := f.Nodes[id]
		if node.Kind != "map" {
			continue
		}
		child, err := mapChild(id, node)
		if err != nil {
			return err
		}
		childNode, exists := f.Nodes[child]
		if !exists {
			return invalid(fmt.Sprintf("flow: map node %q: child node %q does not exist", id, child),
				map[string]any{"node": id, "child": child})
		}
		if childNode.Guard != nil {
			return invalid(fmt.Sprintf("flow: map node %q: child node %q has a guard: a guard counts visits of nodes the machine routes to and a map child runs once per element", id, child),
				map[string]any{"node": id, "child": child})
		}
		switch childNode.Kind {
		case "set", "map", "parallel":
			return invalid(fmt.Sprintf("flow: map node %q: child node %q has kind %q, which the machine runs itself", id, child, childNode.Kind),
				map[string]any{"node": id, "child": child, "kind": childNode.Kind})
		}
		if owner, ok := owners[child]; ok {
			return invalid(fmt.Sprintf("flow: map nodes %q and %q both use child node %q", owner, id, child),
				map[string]any{"maps": []string{owner, id}, "child": child})
		}
		owners[child] = id
	}
	return nil
}

// mapChild returns a map node's child name. The child is decided by the
// spec's "node" field alone: "over" is an expression flow/compile judges, so
// an empty or missing over must not skip the child checks (REQ-FLOW-062). A
// spec whose node is absent or not a non-empty string names no child the
// machine can run, so the compiler rejects it here (EP0-149).
func mapChild(id string, node flow.Node) (string, error) {
	var spec struct {
		Node json.RawMessage `json:"node"`
	}
	var child string
	bad := json.Unmarshal(node.Spec, &spec) != nil ||
		json.Unmarshal(spec.Node, &child) != nil || child == ""
	if bad {
		return "", invalid(fmt.Sprintf("flow: map node %q: spec needs a non-empty \"node\" child name", id),
			map[string]any{"node": id})
	}
	return child, nil
}

func validateReachable(f *flow.IR) error {
	// Outgoing edges are indexed once: rescanning the edge list per dequeued
	// node made validation quadratic in the size of the flow.
	out := make(map[string][]string, len(f.Nodes)+1)
	for _, edge := range f.Edges {
		out[edge.From] = append(out[edge.From], edge.To)
	}
	reached := map[string]bool{Start: true}
	queue := []string{Start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range out[current] {
			if next == Fail || reached[next] {
				continue
			}
			reached[next] = true
			queue = append(queue, next)
		}
	}
	for _, id := range sortedKeys(f.Nodes) {
		if !reached[id] {
			return invalid(fmt.Sprintf("flow: node %q is unreachable from %s", id, Start), map[string]any{"node": id})
		}
	}
	return nil
}

// validateCycles requires at least one bounded node on every cycle
// (REQ-FLOW-003). An empty guard bounds nothing and a while condition only
// chooses when the cycle leaves, so neither counts as a bound.
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

// hasGuardedNode reports whether a node on the cycle carries a finite budget
// the machine can enforce on every lap: max_visits or cost_mpts (REQ-FLOW-003).
// A while condition is not one: it says when the cycle may leave and can hold
// on every lap — {"while": "true"} is a valid predicate that bounds nothing
// (specs/flow-engine/design.md §2.1).
func hasGuardedNode(f *flow.IR, component []string) bool {
	for _, id := range component {
		guard := f.Nodes[id].Guard
		if guard != nil && (guard.MaxVisits > 0 || guard.CostMpts > 0) {
			return true
		}
	}
	return false
}

// stronglyConnected returns the strongly connected components (Tarjan), so a
// cycle with another path out and back is still one component. The search uses
// an explicit stack of frames rather than recursion, so a long chain cannot
// exhaust the goroutine stack.
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
			state.run(id)
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
	frames     []tarjanFrame
	adj        map[string][]string
	components [][]string
}

// tarjanFrame is one node whose outgoing edges are being walked, and the index
// of the next edge to look at.
type tarjanFrame struct {
	id   string
	edge int
}

func (s *tarjanState) push(id string) {
	s.indices[id] = s.next
	s.low[id] = s.next
	s.next++
	s.stack = append(s.stack, id)
	s.onStack[id] = true
	s.frames = append(s.frames, tarjanFrame{id: id})
}

func (s *tarjanState) run(root string) {
	s.push(root)
	for len(s.frames) > 0 {
		frame := &s.frames[len(s.frames)-1]
		if frame.edge < len(s.adj[frame.id]) {
			next := s.adj[frame.id][frame.edge]
			frame.edge++
			if _, seen := s.indices[next]; !seen {
				s.push(next)
			} else if s.onStack[next] && s.indices[next] < s.low[frame.id] {
				s.low[frame.id] = s.indices[next]
			}
			continue
		}
		id := frame.id
		s.frames = s.frames[:len(s.frames)-1]
		if len(s.frames) > 0 {
			parent := s.frames[len(s.frames)-1].id
			if s.low[id] < s.low[parent] {
				s.low[parent] = s.low[id]
			}
		}
		s.closeComponent(id)
	}
}

func (s *tarjanState) closeComponent(id string) {
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
