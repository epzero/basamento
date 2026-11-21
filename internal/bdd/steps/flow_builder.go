package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/builder"
	"github.com/epzero/basamento/flow/compile"
	"github.com/epzero/basamento/flow/ir"
)

// builderWorld holds the declarations of builder.feature's flows, the IR the
// builder produced for them and the compiler's verdict on the equivalent data
// flow.
type builderWorld struct {
	name    string
	version int

	current *flowDeclarations
	other   *flowDeclarations

	first  *flow.IR
	second *flow.IR
	kinds  map[string]string

	data    *compile.Compiled
	dataErr error
	lastErr error
}

type flowDeclarations struct {
	stateSchema json.RawMessage
	stateType   reflect.Type
	nodes       []declaredNode
	reducers    []declaredReducer
	edges       []declaredEdge
	entries     []string
}

type declaredNode struct {
	name  string
	kind  string
	spec  json.RawMessage
	in    string
	out   string
	guard *flow.Guard
}

type declaredReducer struct {
	key     string
	reducer string
}

type declaredEdge struct {
	from string
	to   string
	when string
}

func newBuilderWorld() *builderWorld { return &builderWorld{} }

func builderCtx(ctx context.Context) *builderWorld { return world(ctx).FlowBuilder }

var (
	// declaredNodeHeadRE matches node name and kind. Specs after "given the node
	// spec" are parsed with balanced braces: the old \{[^}]*\} pattern stopped at
	// the first "}", so nested JSON was silently truncated (step grammar: use
	// balanced object literals only).
	declaredNodeHeadRE = regexp.MustCompile(`"([^"]*)"\s+of kind "([^"]*)"`)
	reducerRE          = regexp.MustCompile(`"([^"]*)" as "([^"]*)"`)
)

func unquoteName(name string) string {
	return strings.Trim(strings.TrimSpace(name), `"`)
}

func registerFlowBuilder(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).FlowBuilder = newBuilderWorld()
		return ctx, nil
	})
	sc.Step(`^a flow builder for flow "([^"]*)" version (\d+)$`, func(ctx context.Context, name string, version int) error {
		w := builderCtx(ctx)
		w.name, w.version = name, version
		w.current = &flowDeclarations{}
		w.other = nil
		return nil
	})

	sc.Step(`^a flow built with node ("[^"]*" of kind "[^"]*".*)$`, func(ctx context.Context, rest string) error {
		w := builderCtx(ctx)
		w.resetFlow()
		return w.declare(rest)
	})
	sc.Step(`^a flow built with nodes ("[^"]*" of kind "[^"]*".*)$`, func(ctx context.Context, rest string) error {
		w := builderCtx(ctx)
		w.resetFlow()
		return w.declare(rest)
	})
	sc.Step(`^a flow with node ("[^"]*" of kind "[^"]*".*)$`, func(ctx context.Context, rest string) error {
		// EP0-100: host replay flows share this step text; route instead of a
		// second registration godog would reject as ambiguous.
		full := "a flow with node " + rest
		if m := replayFlowStep.FindStringSubmatch(full); m != nil {
			return declareReplayFlow(ctx, m[1], m[2], m[3], m[4], m[5], m[6])
		}
		w := builderCtx(ctx)
		w.resetFlow()
		return w.declare(rest)
	})
	sc.Step(`^a flow with nodes ("[^"]*" of kind "[^"]*".*)$`, func(ctx context.Context, rest string) error {
		w := builderCtx(ctx)
		w.resetFlow()
		return w.declare(rest)
	})

	sc.Step(`^a flow built with one node of each of the (\d+) node kinds:$`, func(ctx context.Context, count int, table *godog.Table) error {
		w := builderCtx(ctx)
		w.resetFlow()
		w.kinds = map[string]string{}
		for _, row := range table.Rows[1:] {
			if len(row.Cells) < 2 {
				return fmt.Errorf("node row %v has fewer than two cells", row.Cells)
			}
			name := strings.TrimSpace(row.Cells[0].Value)
			kind := strings.TrimSpace(row.Cells[1].Value)
			w.current.nodes = append(w.current.nodes, declaredNode{name: name, kind: kind})
			w.kinds[name] = kind
		}
		if len(w.kinds) != count {
			return fmt.Errorf("declared %d nodes, want %d", len(w.kinds), count)
		}
		return nil
	})

	sc.Step(`^a flow built with the same nodes and reducers declared in the reverse order and the same edges in the same order$`,
		func(ctx context.Context) error {
			w := builderCtx(ctx)
			if w.current == nil {
				return fmt.Errorf("no flow declared")
			}
			w.other = reverseDeclarations(w.current)
			return nil
		})

	sc.Step(`^a state schema with the keys "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		w := builderCtx(ctx)
		w.current.stateSchema = objectSchema(map[string]any{
			first:  map[string]any{},
			second: map[string]any{},
		})
		return nil
	})
	sc.Step(`^a state schema with the key "([^"]*)" of type integer$`, func(ctx context.Context, key string) error {
		w := builderCtx(ctx)
		w.current.stateSchema = objectSchema(map[string]any{
			key: map[string]any{"type": "integer"},
		})
		return nil
	})
	sc.Step(`^a state schema declaring the key "([^"]*)" as the type "([^"]*)"$`, func(ctx context.Context, key, typ string) error {
		w := builderCtx(ctx)
		w.current.stateSchema = objectSchema(map[string]any{
			key: map[string]any{"type": typ},
		})
		return nil
	})

	sc.Step(`^a Go struct type "([^"]*)" with the fields:$`, func(ctx context.Context, name string, table *godog.Table) error {
		fields, err := tableToStructFields(table, world(ctx).Schema.types)
		if err != nil {
			return err
		}
		world(ctx).Schema.types[name] = reflect.StructOf(fields)
		return nil
	})

	sc.Step(`^the reducer of "([^"]*)" declared as "([^"]*)", of "([^"]*)" as "([^"]*)" and of "([^"]*)" as "([^"]*)"$`,
		func(ctx context.Context, first, firstReducer, second, secondReducer, third, thirdReducer string) error {
			w := builderCtx(ctx)
			w.current.reducers = append(w.current.reducers,
				declaredReducer{key: first, reducer: firstReducer},
				declaredReducer{key: second, reducer: secondReducer},
				declaredReducer{key: third, reducer: thirdReducer},
			)
			return nil
		})
	sc.Step(`^the reducer of "([^"]*)" declared as "([^"]*)"$`, func(ctx context.Context, key, reducer string) error {
		w := builderCtx(ctx)
		w.current.reducers = append(w.current.reducers, declaredReducer{key: key, reducer: reducer})
		return nil
	})

	sc.Step(`^node "([^"]*)" of kind "([^"]*)" is declared$`, func(ctx context.Context, name, kind string) error {
		w := builderCtx(ctx)
		w.current.nodes = append(w.current.nodes, declaredNode{name: name, kind: kind})
		return nil
	})
	sc.Step(`^node "([^"]*)" of kind "([^"]*)" is declared in the same flow$`, func(ctx context.Context, name, kind string) error {
		w := builderCtx(ctx)
		w.current.nodes = append(w.current.nodes, declaredNode{name: name, kind: kind})
		return w.buildCurrent()
	})
	sc.Step(`^an edge from "([^"]*)" to "([^"]*)" is declared$`, func(ctx context.Context, from, to string) error {
		w := builderCtx(ctx)
		w.current.edges = append(w.current.edges, declaredEdge{from: from, to: to})
		return w.buildCurrent()
	})
	sc.Step(`^the nodes "([^"]*)" and "([^"]*)" are both marked as the entry node$`, func(ctx context.Context, first, second string) error {
		w := builderCtx(ctx)
		w.current.entries = append(w.current.entries, first, second)
		return w.buildCurrent()
	})
	sc.Step(`^a flow node "([^"]*)" of kind "([^"]*)" is declared with the output "([^"]*)"$`, func(ctx context.Context, name, kind, out string) error {
		w := builderCtx(ctx)
		w.current.nodes = append(w.current.nodes, declaredNode{name: name, kind: kind, out: out})
		return w.buildCurrent()
	})
	sc.Step(`^the node "([^"]*)" is marked as the entry node$`, func(ctx context.Context, name string) error {
		w := builderCtx(ctx)
		w.current.entries = append(w.current.entries, name)
		return nil
	})

	sc.Step(`^an edge (START|[A-Za-z0-9_.-]+) -> "([^"]*)" without a condition$`, func(ctx context.Context, from, to string) error {
		w := builderCtx(ctx)
		w.current.edges = append(w.current.edges, declaredEdge{from: unquoteName(from), to: to})
		return nil
	})
	sc.Step(`^the edges (.+)$`, func(ctx context.Context, rest string) error {
		w := builderCtx(ctx)
		edges, err := parseDeclaredEdges(rest)
		if err != nil {
			return err
		}
		w.current.edges = append(w.current.edges, edges...)
		return nil
	})
	sc.Step(`^node "([^"]*)" of kind "([^"]*)" and the edge (START|[A-Za-z0-9_.-]+) -> "([^"]*)"$`,
		func(ctx context.Context, name, kind, from, to string) error {
			w := builderCtx(ctx)
			w.current.nodes = append(w.current.nodes, declaredNode{name: name, kind: kind})
			w.current.edges = append(w.current.edges, declaredEdge{from: unquoteName(from), to: to})
			return nil
		})
	sc.Step(`^the node "([^"]*)" has the node spec (\{[^}]*\}), the input "([^"]*)", the output "([^"]*)" and the guard of (\d+) visits, (\d+) milli-points and the condition "([^"]*)"$`,
		func(ctx context.Context, name, spec, in, out string, visits, milliPoints int, condition string) error {
			w := builderCtx(ctx)
			for i := range w.current.nodes {
				if w.current.nodes[i].name != name {
					continue
				}
				w.current.nodes[i].spec = json.RawMessage(spec)
				w.current.nodes[i].in = in
				w.current.nodes[i].out = out
				w.current.nodes[i].guard = &flow.Guard{
					MaxVisits: visits,
					CostMpts:  int64(milliPoints),
					While:     condition,
				}
				return nil
			}
			return fmt.Errorf("node %q is not declared", name)
		})

	sc.Step(`^the flow is built$`, func(ctx context.Context) error {
		return builderCtx(ctx).buildCurrent()
	})
	sc.Step(`^both flows are built$`, func(ctx context.Context) error {
		return builderCtx(ctx).buildBoth()
	})
	sc.Step(`^a flow is built with those same declarations$`, func(ctx context.Context) error {
		return builderCtx(ctx).buildAgain()
	})
	sc.Step(`^the flow is built declaring its state as the Go type "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := builderCtx(ctx)
		t, ok := world(ctx).Schema.types[name]
		if !ok {
			return fmt.Errorf("no declared Go type %q", name)
		}
		w.current.stateType = t
		return w.buildCurrent()
	})
	sc.Step(`^the flow "([^"]*)" version (\d+) written as data with the same declarations is compiled$`,
		func(ctx context.Context, name string, version int) error {
			w := builderCtx(ctx)
			if name != w.name || version != w.version {
				return fmt.Errorf("data flow %q version %d, want %q version %d", name, version, w.name, w.version)
			}
			w.data, w.dataErr = w.compileData()
			return nil
		})

	sc.Step(`^the canonical IR of the built flow equals the canonical IR of the data flow$`, func(ctx context.Context) error {
		w := builderCtx(ctx)
		if err := w.ensureFirst(); err != nil {
			return err
		}
		if w.dataErr != nil {
			return w.dataErr
		}
		if w.data == nil {
			return fmt.Errorf("the data flow was not compiled")
		}
		built, err := ir.Canonical(w.first)
		if err != nil {
			return err
		}
		if !bytes.Equal(built, w.data.Canonical()) {
			return fmt.Errorf("canonical IR differs:\n built %s\n data  %s", built, w.data.Canonical())
		}
		return nil
	})
	sc.Step(`^the canonical IR of the built flow gives node "([^"]*)" the spec (\{[^}]*\}) and node "([^"]*)" the spec (\{[^}]*\})$`,
		func(ctx context.Context, firstName, firstSpec, secondName, secondSpec string) error {
			w := builderCtx(ctx)
			if err := w.ensureFirst(); err != nil {
				return err
			}
			for _, pair := range [][2]string{{firstName, firstSpec}, {secondName, secondSpec}} {
				if err := assertNodeSpec(w.first, pair[0], pair[1]); err != nil {
					return err
				}
			}
			return nil
		})
	sc.Step(`^the content hash of the built flow equals the content hash of the data flow$`, func(ctx context.Context) error {
		w := builderCtx(ctx)
		if err := w.ensureFirst(); err != nil {
			return err
		}
		if w.data == nil {
			return fmt.Errorf("the data flow was not compiled")
		}
		hash, err := ir.Hash(w.first)
		if err != nil {
			return err
		}
		if hash != w.data.Hash() {
			return fmt.Errorf("content hash %s, want %s", hash, w.data.Hash())
		}
		return nil
	})

	sc.Step(`^the IR bytes of the second flow equal the IR bytes of the first flow$`, func(ctx context.Context) error {
		w := builderCtx(ctx)
		return w.equalCanonical()
	})
	sc.Step(`^the IR bytes of the second build equal the IR bytes of the first build$`, func(ctx context.Context) error {
		w := builderCtx(ctx)
		return w.equalCanonical()
	})

	sc.Step(`^the IR has exactly (\d+) nodes$`, func(ctx context.Context, count int) error {
		w := builderCtx(ctx)
		if err := w.ensureFirst(); err != nil {
			return err
		}
		if len(w.first.Nodes) != count {
			return fmt.Errorf("IR has %d nodes, want %d", len(w.first.Nodes), count)
		}
		return nil
	})
	sc.Step(`^the IR gives each node its declared kind$`, func(ctx context.Context) error {
		w := builderCtx(ctx)
		if err := w.ensureFirst(); err != nil {
			return err
		}
		for name, kind := range w.kinds {
			if got := w.first.Nodes[name].Kind; got != kind {
				return fmt.Errorf("node %q kind %q, want %q", name, got, kind)
			}
		}
		return nil
	})
	sc.Step(`^the node "([^"]*)" in the IR has the node spec (\{[^}]*\}), the input "([^"]*)", the output "([^"]*)" and the guard of (\d+) visits, (\d+) milli-points and the condition "([^"]*)"$`,
		func(ctx context.Context, name, spec, in, out string, visits, milliPoints int, condition string) error {
			w := builderCtx(ctx)
			if err := w.ensureFirst(); err != nil {
				return err
			}
			node, ok := w.first.Nodes[name]
			if !ok {
				return fmt.Errorf("IR has no node %q", name)
			}
			if err := assertNodeSpec(w.first, name, spec); err != nil {
				return err
			}
			if node.In != in {
				return fmt.Errorf("node %q input %q, want %q", name, node.In, in)
			}
			if node.Out != out {
				return fmt.Errorf("node %q output %q, want %q", name, node.Out, out)
			}
			want := flow.Guard{MaxVisits: visits, CostMpts: int64(milliPoints), While: condition}
			if node.Guard == nil || *node.Guard != want {
				return fmt.Errorf("node %q guard %+v, want %+v", name, node.Guard, want)
			}
			return nil
		})

	sc.Step(`^the IR has (\d+) edges in the order (.+)$`, func(ctx context.Context, count int, rest string) error {
		w := builderCtx(ctx)
		if err := w.ensureFirst(); err != nil {
			return err
		}
		if len(w.first.Edges) != count {
			return fmt.Errorf("IR has %d edges, want %d", len(w.first.Edges), count)
		}
		want, err := parseDeclaredEdges(rest)
		if err != nil {
			return err
		}
		if len(want) != count {
			return fmt.Errorf("parsed %d edges from %q, want %d", len(want), rest, count)
		}
		for i, edge := range want {
			got := w.first.Edges[i]
			if got.From != edge.from || got.To != edge.to {
				return fmt.Errorf("edge %d is %q -> %q, want %q -> %q", i, got.From, got.To, edge.from, edge.to)
			}
		}
		return nil
	})
	sc.Step(`^the IR gives edge "([^"]*)" -> "([^"]*)" the condition "([^"]*)", edge "([^"]*)" -> "([^"]*)" the condition "([^"]*)" and every other edge no condition$`,
		func(ctx context.Context, firstFrom, firstTo, firstWhen, secondFrom, secondTo, secondWhen string) error {
			w := builderCtx(ctx)
			if err := w.ensureFirst(); err != nil {
				return err
			}
			conditions := map[[2]string]string{
				{firstFrom, firstTo}:   firstWhen,
				{secondFrom, secondTo}: secondWhen,
			}
			for _, edge := range w.first.Edges {
				want, ok := conditions[[2]string{edge.From, edge.To}]
				if !ok {
					want = ""
				}
				if edge.When != want {
					return fmt.Errorf("edge %q -> %q condition %q, want %q", edge.From, edge.To, edge.When, want)
				}
			}
			return nil
		})
	sc.Step(`^the IR has exactly one edge from "([^"]*)" to "([^"]*)" and exactly one edge from "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, firstFrom, firstTo, secondFrom, secondTo string) error {
			w := builderCtx(ctx)
			if err := w.ensureFirst(); err != nil {
				return err
			}
			for _, pair := range [][2]string{{firstFrom, firstTo}, {secondFrom, secondTo}} {
				if count := countEdges(w.first, pair[0], pair[1]); count != 1 {
					return fmt.Errorf("IR has %d edges from %q to %q, want 1", count, pair[0], pair[1])
				}
			}
			return nil
		})
	sc.Step(`^the IR has an edge from "([^"]*)" to "([^"]*)" with no condition$`, func(ctx context.Context, from, to string) error {
		w := builderCtx(ctx)
		if err := w.ensureFirst(); err != nil {
			return err
		}
		for _, edge := range w.first.Edges {
			if edge.From == from && edge.To == to && edge.When == "" {
				return nil
			}
		}
		return fmt.Errorf("IR has no unconditional edge from %q to %q", from, to)
	})

	sc.Step(`^the state schema of the built flow equals the schema generated from "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := builderCtx(ctx)
		if err := w.ensureFirst(); err != nil {
			return err
		}
		t, ok := world(ctx).Schema.types[name]
		if !ok {
			return fmt.Errorf("no declared Go type %q", name)
		}
		generated, err := schema.ForType(t)
		if err != nil {
			return err
		}
		got, err := schema.Canonicalize(w.first.State.Schema)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, generated.Raw) {
			return fmt.Errorf("state schema\n got %s\nwant %s", got, generated.Raw)
		}
		return nil
	})
	sc.Step(`^the reducers of the built flow are messages "([^"]*)", files "([^"]*)" and attempts "([^"]*)"$`,
		func(ctx context.Context, messages, files, attempts string) error {
			w := builderCtx(ctx)
			if err := w.ensureFirst(); err != nil {
				return err
			}
			want := map[string]string{
				"messages": messages,
				"files":    files,
				"attempts": attempts,
			}
			if !reflect.DeepEqual(w.first.State.Reducers, want) {
				return fmt.Errorf("reducers %+v, want %+v", w.first.State.Reducers, want)
			}
			return nil
		})

	sc.Step(`^the build fails with code "([^"]*)" naming the node "([^"]*)"$`, func(ctx context.Context, code, name string) error {
		platform, err := builderCtx(ctx).requireFailure(code)
		if err != nil {
			return err
		}
		if want := fmt.Sprintf("node %q", name); !strings.Contains(platform.Message, want) {
			return fmt.Errorf("message %q does not contain %q", platform.Message, want)
		}
		return nil
	})
	sc.Step(`^the build fails with code "([^"]*)" naming the nodes "([^"]*)" and "([^"]*)"$`,
		func(ctx context.Context, code, first, second string) error {
			platform, err := builderCtx(ctx).requireFailure(code)
			if err != nil {
				return err
			}
			for _, name := range []string{first, second} {
				if want := fmt.Sprintf("node %q", name); !strings.Contains(platform.Message, want) {
					return fmt.Errorf("message %q does not contain %q", platform.Message, want)
				}
			}
			return nil
		})
	sc.Step(`^the build fails with code "([^"]*)" stating that the flow has no entry$`, func(ctx context.Context, code string) error {
		platform, err := builderCtx(ctx).requireFailure(code)
		if err != nil {
			return err
		}
		if !strings.Contains(platform.Message, "no entry") {
			return fmt.Errorf("message %q does not state there is no entry", platform.Message)
		}
		return nil
	})
	sc.Step(`^the build fails with code "([^"]*)" naming the node "([^"]*)" and the key "([^"]*)"$`,
		func(ctx context.Context, code, name, key string) error {
			platform, err := builderCtx(ctx).requireFailure(code)
			if err != nil {
				return err
			}
			for _, want := range []string{fmt.Sprintf("node %q", name), fmt.Sprintf("%q", key)} {
				if !strings.Contains(platform.Message, want) {
					return fmt.Errorf("message %q does not contain %q", platform.Message, want)
				}
			}
			return nil
		})
	sc.Step(`^the build fails with code "([^"]*)" naming the state key "([^"]*)"$`, func(ctx context.Context, code, key string) error {
		platform, err := builderCtx(ctx).requireFailure(code)
		if err != nil {
			return err
		}
		if want := fmt.Sprintf("state key %q", key); !strings.Contains(platform.Message, want) {
			return fmt.Errorf("message %q does not contain %q", platform.Message, want)
		}
		return nil
	})
	sc.Step(`^the build fails with code "([^"]*)" at schema location "([^"]*)"$`, func(ctx context.Context, code, location string) error {
		platform, err := builderCtx(ctx).requireFailure(code)
		if err != nil {
			return err
		}
		if got := schema.ErrLocation(platform); got != location {
			return fmt.Errorf("schema location %q, want %q: %s", got, location, platform.Message)
		}
		return nil
	})
}

// declare appends the nodes, reducers and edges of one phrase to the current
// flow. The markers keep the three lists apart without a grammar.
func (w *builderWorld) declare(rest string) error {
	nodesText, reducersText, edgesText := splitDeclarations(rest)
	nodes, err := parseDeclaredNodes(nodesText)
	if err != nil {
		return fmt.Errorf("declare nodes: %w", err)
	}
	w.current.nodes = append(w.current.nodes, nodes...)
	for _, match := range reducerRE.FindAllStringSubmatch(reducersText, -1) {
		w.current.reducers = append(w.current.reducers, declaredReducer{key: match[1], reducer: match[2]})
	}
	if edgesText != "" {
		edges, err := parseDeclaredEdges(edgesText)
		if err != nil {
			return err
		}
		w.current.edges = append(w.current.edges, edges...)
	}
	return nil
}

func parseDeclaredNodes(text string) ([]declaredNode, error) {
	matches := declaredNodeHeadRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no node in %q", text)
	}
	nodes := make([]declaredNode, 0, len(matches))
	for i, loc := range matches {
		name := text[loc[2]:loc[3]]
		kind := text[loc[4]:loc[5]]
		tailStart := loc[1]
		tailEnd := len(text)
		if i+1 < len(matches) {
			tailEnd = matches[i+1][0]
		}
		tail := strings.TrimSpace(text[tailStart:tailEnd])
		spec, err := parseNodeTail(tail)
		if err != nil {
			return nil, fmt.Errorf("node %q of kind %q: %w", name, kind, err)
		}
		nodes = append(nodes, declaredNode{name: name, kind: kind, spec: spec})
	}
	return nodes, nil
}

func parseNodeTail(tail string) (json.RawMessage, error) {
	const prefix = "given the node spec "
	tail = strings.TrimSpace(tail)
	if tail == "" {
		return nil, nil
	}
	if nodeListContinuation(tail) {
		return nil, nil
	}
	if !strings.HasPrefix(tail, prefix) {
		return nil, fmt.Errorf("unexpected clause %q after node", tail)
	}
	rest := strings.TrimSpace(tail[len(prefix):])
	if !strings.HasPrefix(rest, "{") {
		return nil, fmt.Errorf("node spec must be a JSON object, got %q", rest)
	}
	end, ok := balancedJSONObjectEnd(rest)
	if !ok {
		return nil, fmt.Errorf("node spec has unbalanced braces in %q", rest)
	}
	spec := json.RawMessage(rest[:end])
	extra := strings.TrimSpace(rest[end:])
	if extra != "" && !nodeListContinuation(extra) {
		return nil, fmt.Errorf("nested or trailing text after the node spec %q (step grammar supports one JSON object per spec)", extra)
	}
	return spec, nil
}

// nodeListContinuation is text between two node heads in a step phrase (and node, comma, and).
func nodeListContinuation(s string) bool {
	switch strings.TrimSpace(s) {
	case "", "and", "and node", ",":
		return true
	default:
		return false
	}
}

func balancedJSONObjectEnd(s string) (int, bool) {
	if s == "" || s[0] != '{' {
		return 0, false
	}
	depth := 0
	inString := false
	escape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if inString {
			switch c {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func splitDeclarations(rest string) (nodes, reducers, edges string) {
	type marker struct {
		text  string
		index int
	}
	reducerAt := marker{text: ", reducers ", index: strings.Index(rest, ", reducers ")}
	edgeAt := marker{index: -1}
	for _, text := range []string{", and edges ", " and edges ", ", edges ", " and the edge "} {
		if i := strings.Index(rest, text); i >= 0 && (edgeAt.index < 0 || i < edgeAt.index) {
			edgeAt = marker{text: text, index: i}
		}
	}
	switch {
	case reducerAt.index >= 0 && (edgeAt.index < 0 || reducerAt.index < edgeAt.index):
		nodes = rest[:reducerAt.index]
		reducerEnd := len(rest)
		if edgeAt.index >= 0 {
			reducerEnd = edgeAt.index
		}
		reducers = rest[reducerAt.index+len(reducerAt.text) : reducerEnd]
		if edgeAt.index >= 0 {
			edges = rest[edgeAt.index+len(edgeAt.text):]
		}
	case edgeAt.index >= 0:
		nodes = rest[:edgeAt.index]
		edges = rest[edgeAt.index+len(edgeAt.text):]
	default:
		nodes = rest
	}
	return nodes, reducers, edges
}

func parseDeclaredEdges(text string) ([]declaredEdge, error) {
	if strings.Contains(text, "when ") || strings.Contains(text, "without a condition") {
		return parseEdgeClauses(text)
	}
	return parseEdgePaths(text)
}

// parseEdgePaths reads edge chains such as START -> "plan" -> FAIL and yields
// one edge for each consecutive pair.
func parseEdgePaths(text string) ([]declaredEdge, error) {
	var edges []declaredEdge
	for _, chunk := range splitClauses(text) {
		names := strings.Split(chunk, "->")
		if len(names) < 2 {
			return nil, fmt.Errorf("no edge in %q", chunk)
		}
		for i := 0; i+1 < len(names); i++ {
			edges = append(edges, declaredEdge{
				from: unquoteName(names[i]),
				to:   unquoteName(names[i+1]),
			})
		}
	}
	if len(edges) == 0 {
		return nil, fmt.Errorf("no edge in %q", text)
	}
	return edges, nil
}

// parseEdgeClauses reads one edge per clause, with an optional condition.
func parseEdgeClauses(text string) ([]declaredEdge, error) {
	var edges []declaredEdge
	for _, chunk := range splitClauses(text) {
		when := ""
		if i := strings.Index(chunk, " when "); i >= 0 {
			when = strings.Trim(strings.TrimSpace(chunk[i+len(" when "):]), `"`)
			chunk = chunk[:i]
		} else if i := strings.Index(chunk, " without a condition"); i >= 0 {
			chunk = chunk[:i]
		}
		names := strings.Split(chunk, "->")
		if len(names) != 2 {
			return nil, fmt.Errorf("no edge in %q", chunk)
		}
		edges = append(edges, declaredEdge{
			from: unquoteName(names[0]),
			to:   unquoteName(names[1]),
			when: when,
		})
	}
	if len(edges) == 0 {
		return nil, fmt.Errorf("no edge in %q", text)
	}
	return edges, nil
}

// splitClauses splits an edge list at top-level commas and " and " only, so a
// condition such as "state.a > 0 and state.b > 0" stays in one clause.
func splitClauses(text string) []string {
	var clauses []string
	start := 0
	var quote byte
	escaped := false
	for i := 0; i < len(text); {
		c := text[i]
		if quote != 0 {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == quote:
				quote = 0
			}
			i++
			continue
		}
		switch {
		case c == '"' || c == '\'':
			quote = c
			i++
		case c == ',':
			addClause(&clauses, text[start:i])
			i++
			start = i
		case strings.HasPrefix(text[i:], " and "):
			addClause(&clauses, text[start:i])
			i += len(" and ")
			start = i
		default:
			i++
		}
	}
	addClause(&clauses, text[start:])
	return clauses
}

func addClause(clauses *[]string, text string) {
	if clause := strings.TrimSpace(text); clause != "" {
		*clauses = append(*clauses, clause)
	}
}

func objectSchema(properties map[string]any) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"type":       "object",
		"properties": properties,
	})
	if err != nil {
		panic(err) // the map holds only JSON values
	}
	return raw
}

func reverseDeclarations(in *flowDeclarations) *flowDeclarations {
	out := &flowDeclarations{
		stateSchema: in.stateSchema,
		stateType:   in.stateType,
		edges:       append([]declaredEdge(nil), in.edges...),
		entries:     append([]string(nil), in.entries...),
	}
	for i := len(in.nodes) - 1; i >= 0; i-- {
		out.nodes = append(out.nodes, in.nodes[i])
	}
	for i := len(in.reducers) - 1; i >= 0; i-- {
		out.reducers = append(out.reducers, in.reducers[i])
	}
	return out
}

func (w *builderWorld) resetFlow() {
	w.current.nodes = nil
	w.current.reducers = nil
	w.current.edges = nil
	w.current.entries = nil
	w.kinds = nil
	w.first = nil
	w.second = nil
	w.data = nil
	w.dataErr = nil
	w.lastErr = nil
}

func (w *builderWorld) builder(decls *flowDeclarations) *builder.Builder {
	b := builder.New(w.name, w.version)
	if decls.stateType != nil {
		b.StateType(decls.stateType)
	} else if len(decls.stateSchema) > 0 {
		b.StateSchema(decls.stateSchema)
	}
	for _, reducer := range decls.reducers {
		b.Reducer(reducer.key, reducer.reducer)
	}
	for _, node := range decls.nodes {
		var opts []builder.NodeOption
		if node.in != "" {
			opts = append(opts, builder.In(node.in))
		}
		if node.out != "" {
			opts = append(opts, builder.Out(node.out))
		}
		if node.guard != nil {
			opts = append(opts, builder.Guard(*node.guard))
		}
		b.Node(node.name, builder.Kind(node.kind), node.spec, opts...)
	}
	for _, edge := range decls.edges {
		b.Edge(edge.from, edge.to, edge.when)
	}
	for _, entry := range decls.entries {
		b.Entry(entry)
	}
	return b
}

func (w *builderWorld) buildCurrent() error {
	if w.current == nil {
		return fmt.Errorf("no flow declared")
	}
	w.first, w.lastErr = w.builder(w.current).Build()
	return nil
}

func (w *builderWorld) buildBoth() error {
	if w.other == nil {
		return fmt.Errorf("no second flow declared")
	}
	if err := w.buildCurrent(); err != nil {
		return err
	}
	if w.lastErr != nil {
		return nil
	}
	w.second, w.lastErr = w.builder(w.other).Build()
	return nil
}

func (w *builderWorld) buildAgain() error {
	if w.first == nil && w.lastErr == nil {
		if err := w.buildCurrent(); err != nil {
			return err
		}
	}
	if w.lastErr != nil {
		return nil
	}
	w.second, w.lastErr = w.builder(w.current).Build()
	return nil
}

func (w *builderWorld) ensureFirst() error {
	if w.first != nil {
		return nil
	}
	if w.lastErr != nil {
		return w.lastErr
	}
	if err := w.buildCurrent(); err != nil {
		return err
	}
	if w.lastErr != nil {
		return w.lastErr
	}
	return nil
}

func (w *builderWorld) equalCanonical() error {
	if err := w.ensureFirst(); err != nil {
		return err
	}
	if w.second == nil {
		return fmt.Errorf("the second flow was not built")
	}
	first, err := ir.Canonical(w.first)
	if err != nil {
		return err
	}
	second, err := ir.Canonical(w.second)
	if err != nil {
		return err
	}
	if !bytes.Equal(first, second) {
		return fmt.Errorf("IR bytes differ:\n first  %s\n second %s", first, second)
	}
	return nil
}

func (w *builderWorld) requireFailure(code string) (*core.Error, error) {
	if w.lastErr == nil {
		return nil, fmt.Errorf("the build succeeded, want a failure")
	}
	var platform *core.Error
	if !errors.As(w.lastErr, &platform) {
		return nil, fmt.Errorf("error is not a *core.Error: %w", w.lastErr)
	}
	if string(platform.Code) != code {
		return nil, fmt.Errorf("code %s, want %s: %s", platform.Code, code, platform.Message)
	}
	return platform, nil
}

func (w *builderWorld) compileData() (*compile.Compiled, error) {
	if w.current == nil {
		return nil, fmt.Errorf("no flow declared")
	}
	stateSchema := w.current.stateSchema
	if w.current.stateType != nil {
		generated, err := schema.ForType(w.current.stateType)
		if err != nil {
			return nil, err
		}
		stateSchema = generated.Raw
	}
	if len(stateSchema) == 0 {
		stateSchema = json.RawMessage(`{"type":"object"}`)
	}
	nodes := make(map[string]flow.Node, len(w.current.nodes))
	for _, node := range w.current.nodes {
		nodes[node.name] = flow.Node{
			Kind:  node.kind,
			In:    node.in,
			Out:   node.out,
			Spec:  node.spec,
			Guard: node.guard,
		}
	}
	var reducers map[string]string
	if len(w.current.reducers) > 0 {
		reducers = make(map[string]string, len(w.current.reducers))
		for _, reducer := range w.current.reducers {
			reducers[reducer.key] = reducer.reducer
		}
	}
	edges := make([]flow.Edge, 0, len(w.current.edges)+len(w.current.entries))
	for _, edge := range w.current.edges {
		edges = append(edges, flow.Edge{From: edge.from, To: edge.to, When: edge.when})
	}
	for _, entry := range w.current.entries {
		edges = append(edges, flow.Edge{From: builder.Start, To: entry})
	}
	data := &flow.IR{
		Name:    w.name,
		Version: w.version,
		State:   flow.StateSpec{Schema: stateSchema, Reducers: reducers},
		Nodes:   nodes,
		Edges:   edges,
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return compile.CompileJSON(raw)
}

func assertNodeSpec(built *flow.IR, name, want string) error {
	node, ok := built.Nodes[name]
	if !ok {
		return fmt.Errorf("IR has no node %q", name)
	}
	got, err := schema.Canonicalize(node.Spec)
	if err != nil {
		return fmt.Errorf("node %q spec: %w", name, err)
	}
	expected, err := schema.Canonicalize(json.RawMessage(want))
	if err != nil {
		return fmt.Errorf("expected spec %q: %w", want, err)
	}
	if !bytes.Equal(got, expected) {
		return fmt.Errorf("node %q spec %s, want %s", name, got, expected)
	}
	return nil
}

func countEdges(f *flow.IR, from, to string) int {
	count := 0
	for _, edge := range f.Edges {
		if edge.From == from && edge.To == to {
			count++
		}
	}
	return count
}
