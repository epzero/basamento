package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/compile"
	"github.com/epzero/basamento/flow/ir"
)

// flowWorld holds the flow definition a compile scenario builds and the
// compiler's verdict on it.
type flowWorld struct {
	ir       *flow.IR
	compiled *compile.Compiled
	lastErr  error

	lastFrom string
	lastTo   string
	lastWhen string
}

func newFlowWorld() *flowWorld { return &flowWorld{} }

func newIR(name string) *flow.IR {
	return &flow.IR{
		Name:    name,
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{},
		Edges:   []flow.Edge{},
	}
}

// addMapFlow creates the flow around a map node: it enters at START, routes
// out to FAIL and leaves the child undeclared, so a scenario names that child's
// kind or guard itself, or leaves it missing (FLOW-062-01).
func (w *flowWorld) addMapFlow(mapNode, stateKey, child string) error {
	w.ir = newIR("maps")
	return w.addMap(mapNode, stateKey, child, false)
}

// addMapNode turns an existing node into a map node, or declares one. A second
// map node (FLOW-064-01) is reached by an edge the scenario declares, and
// declares its child so the scenario exercises the shared-child check rather
// than the missing-child one.
func (w *flowWorld) addMapNode(mapNode, stateKey, child string) error {
	return w.addMap(mapNode, stateKey, child, true)
}

// addMap sets the node's map spec and adds its route out to FAIL. The flow's
// single START edge is added only when the flow has none.
func (w *flowWorld) addMap(mapNode, stateKey, child string, declareChild bool) error {
	if w.ir == nil {
		return fmt.Errorf("no flow declared")
	}
	spec, err := json.Marshal(map[string]any{
		"over": "state." + stateKey,
		"node": child,
	})
	if err != nil {
		return err
	}
	node := w.ir.Nodes[mapNode]
	node.Kind = "map"
	node.Spec = spec
	w.ir.Nodes[mapNode] = node
	started := false
	for _, edge := range w.ir.Edges {
		if edge.From == ir.Start {
			started = true
			break
		}
	}
	if !started {
		w.addEdge(ir.Start, mapNode, "")
	}
	if declareChild {
		w.addNode(child)
	}
	w.addEdge(mapNode, ir.Fail, "")
	return nil
}

func (w *flowWorld) addNode(name string) {
	if name == ir.Start || name == ir.Fail {
		return
	}
	if _, ok := w.ir.Nodes[name]; !ok {
		w.ir.Nodes[name] = flow.Node{Kind: "task"}
	}
}

func (w *flowWorld) addEdge(from, to, when string) {
	w.addNode(from)
	w.addNode(to)
	w.ir.Edges = append(w.ir.Edges, flow.Edge{From: from, To: to, When: when})
}

func (w *flowWorld) failure(code string) (*core.Error, error) {
	if w.lastErr == nil {
		return nil, fmt.Errorf("compilation succeeded, want a failure")
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

var edgeClause = regexp.MustCompile(`from "([^"]*)" to "([^"]*)"`)
var guardCondition = regexp.MustCompile(`^a flow where node "([^"]*)" routes back to node "([^"]*)" and only "([^"]*)" has a guard whose condition is "([^"]*)"$`)
var guardBudget = regexp.MustCompile(`^a flow where node "([^"]*)" routes back to node "([^"]*)" and only "([^"]*)" has a guard of (-?\d+) (visits|milli-points)$`)
var guardBothBudgets = regexp.MustCompile(`^a flow where node "([^"]*)" routes back to node "([^"]*)" and only "([^"]*)" has a guard of (\d+) visits and (\d+) milli-points$`)

// cycleFlow is the cycle every guard scenario describes — START -> to -> back
// -> to — so the only thing a scenario varies is the guard on one of the nodes.
func (w *flowWorld) cycleFlow(back, to string) {
	w.ir = newIR("cycle")
	w.addEdge(ir.Start, to, "")
	w.addEdge(to, back, "")
	w.addEdge(back, to, "")
}

// guard sets the guard on one node of the declared flow.
func (w *flowWorld) guard(name string, guard *flow.Guard) error {
	node, ok := w.ir.Nodes[name]
	if !ok {
		return fmt.Errorf("no node %q to guard", name)
	}
	node.Guard = guard
	w.ir.Nodes[name] = node
	return nil
}

// assertMapChildFailure checks a map-child rejection: the code, the map node
// and the child it names.
func assertMapChildFailure(w *flowWorld, code, mapNode, child string) error {
	platform, err := w.failure(code)
	if err != nil {
		return err
	}
	for _, want := range []string{fmt.Sprintf("map node %q", mapNode), fmt.Sprintf("child node %q", child)} {
		if !strings.Contains(platform.Message, want) {
			return fmt.Errorf("message %q does not contain %q", platform.Message, want)
		}
	}
	return nil
}

func registerFlowCompile(sc *godog.ScenarioContext) {
	sc.Step(`^a flow where node "([^"]*)" routes back to node "([^"]*)" and neither has a guard$`,
		func(ctx context.Context, back, to string) error {
			w := world(ctx).Flow
			w.ir = newIR("cycle")
			w.addEdge(ir.Start, to, "")
			w.addEdge(to, back, "")
			w.addEdge(back, to, "")
			return nil
		})

	sc.Step(guardCondition.String(), func(ctx context.Context, back, to, guarded, condition string) error {
		w := world(ctx).Flow
		w.cycleFlow(back, to)
		return w.guard(guarded, &flow.Guard{While: condition})
	})

	sc.Step(guardBudget.String(), func(ctx context.Context, back, to, guarded string, value int, unit string) error {
		w := world(ctx).Flow
		w.cycleFlow(back, to)
		guard := &flow.Guard{}
		if unit == "visits" {
			guard.MaxVisits = value
		} else {
			guard.CostMpts = int64(value)
		}
		return w.guard(guarded, guard)
	})

	sc.Step(guardBothBudgets.String(), func(ctx context.Context, back, to, guarded string, visits, cost int) error {
		w := world(ctx).Flow
		w.cycleFlow(back, to)
		return w.guard(guarded, &flow.Guard{MaxVisits: visits, CostMpts: int64(cost)})
	})

	sc.Step(`^a flow whose state schema declares "([^"]*)" as an integer$`,
		func(ctx context.Context, key string) error {
			w := world(ctx).Flow
			w.ir = newIR("typed")
			w.ir.State.Schema = json.RawMessage(fmt.Sprintf(
				`{"type":"object","properties":{%q:{"type":"integer"}}}`, key))
			w.addEdge(ir.Start, "plan", "")
			w.addEdge("plan", "check", "")
			w.addEdge("check", ir.Fail, "")
			return nil
		})

	sc.Step(`^a flow with a map node "([^"]*)" that fans out over "state\.([^"]*)" to node "([^"]*)"$`,
		func(ctx context.Context, mapNode, stateKey, child string) error {
			return world(ctx).Flow.addMapFlow(mapNode, stateKey, child)
		})

	sc.Step(`^a map node "([^"]*)" that fans out over "state\.([^"]*)" to node "([^"]*)"$`,
		func(ctx context.Context, mapNode, stateKey, child string) error {
			return world(ctx).Flow.addMapNode(mapNode, stateKey, child)
		})

	sc.Step(`^an edge from node "([^"]*)" to node "([^"]*)"$`,
		func(ctx context.Context, from, to string) error {
			w := world(ctx).Flow
			if w.ir == nil {
				return fmt.Errorf("no flow declared")
			}
			w.addEdge(from, to, "")
			return nil
		})

	sc.Step(`^the node "([^"]*)" is of kind "([^"]*)"$`,
		func(ctx context.Context, name, kind string) error {
			w := world(ctx).Flow
			if w.ir == nil {
				return fmt.Errorf("no flow declared")
			}
			w.addNode(name)
			node := w.ir.Nodes[name]
			node.Kind = kind
			w.ir.Nodes[name] = node
			return nil
		})

	sc.Step(`^the node "([^"]*)" has a guard of (\d+) visits$`,
		func(ctx context.Context, name string, visits int) error {
			w := world(ctx).Flow
			if w.ir == nil {
				return fmt.Errorf("no flow declared")
			}
			w.addNode(name)
			node := w.ir.Nodes[name]
			node.Guard = &flow.Guard{MaxVisits: visits}
			w.ir.Nodes[name] = node
			return nil
		})

	sc.Step(`^a flow whose edge from "([^"]*)" to "([^"]*)" is conditional on "([^"]*)"$`,
		func(ctx context.Context, from, to, when string) error {
			w := world(ctx).Flow
			w.ir = newIR("conditional-entry")
			w.addEdge(from, to, when)
			w.addEdge(to, ir.Fail, "")
			return nil
		})

	sc.Step(`^an edge with condition "([^"]*)"$`, func(ctx context.Context, when string) error {
		w := world(ctx).Flow
		if w.ir == nil {
			return fmt.Errorf("no flow declared")
		}
		w.addEdge("plan", "check", when)
		w.lastFrom, w.lastTo, w.lastWhen = "plan", "check", when
		return nil
	})

	sc.Step(`^a flow whose edges are (.+)$`, func(ctx context.Context, list string) error {
		w := world(ctx).Flow
		w.ir = newIR("edges")
		clauses := strings.Split(list, ",")
		for _, clause := range clauses {
			clause = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(clause), "and "))
			found := edgeClause.FindStringSubmatch(clause)
			if found == nil {
				return fmt.Errorf("no edge in %q", clause)
			}
			w.addEdge(found[1], found[2], "")
		}
		return nil
	})

	sc.Step(`^a flow that declares a node named "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx).Flow
		w.ir = newIR("reserved")
		// Declared verbatim: a reserved name must reach the compiler, not be
		// filtered while the scenario is set up.
		w.ir.Nodes[name] = flow.Node{Kind: "task"}
		w.addNode("plan")
		w.addEdge(ir.Start, "plan", "")
		w.addEdge("plan", ir.Fail, "")
		return nil
	})

	sc.Step(`^a flow with nodes "([^"]*)" and "([^"]*)" and (\d+) edges from "START"$`,
		func(ctx context.Context, first, second string, count int) error {
			w := world(ctx).Flow
			w.ir = newIR("entries")
			w.addNode(first)
			w.addNode(second)
			for i := 0; i < count; i++ {
				w.addEdge(ir.Start, first, "")
			}
			return nil
		})

	sc.Step(`^the flow is compiled$`, func(ctx context.Context) error {
		w := world(ctx).Flow
		if w.ir == nil {
			return fmt.Errorf("no flow declared")
		}
		w.compiled, w.lastErr = compile.Compile(w.ir)
		return nil
	})

	sc.Step(`^compilation succeeds$`, func(ctx context.Context) error {
		w := world(ctx).Flow
		if w.lastErr != nil {
			return fmt.Errorf("compilation failed: %w", w.lastErr)
		}
		if w.compiled == nil {
			return fmt.Errorf("no compiled flow")
		}
		return nil
	})

	sc.Step(`^the compiled flow has no node "([^"]*)" and no node "([^"]*)"$`,
		func(ctx context.Context, first, second string) error {
			w := world(ctx).Flow
			if w.compiled == nil {
				return fmt.Errorf("no compiled flow")
			}
			for _, name := range []string{first, second} {
				if _, ok := w.compiled.IR().Nodes[name]; ok {
					return fmt.Errorf("compiled flow has node %q", name)
				}
			}
			return nil
		})

	sc.Step(`^compilation fails naming node "([^"]*)" and the missing guard$`,
		func(ctx context.Context, name string) error {
			platform, err := world(ctx).Flow.failure(string(core.CodeInvalidArgument))
			if err != nil {
				return err
			}
			for _, want := range []string{fmt.Sprintf("node %q", name), "guard"} {
				if !strings.Contains(platform.Message, want) {
					return fmt.Errorf("message %q does not contain %q", platform.Message, want)
				}
			}
			return nil
		})

	sc.Step(`^compilation fails naming the edge and the expression$`, func(ctx context.Context) error {
		w := world(ctx).Flow
		platform, err := w.failure(string(core.CodeInvalidArgument))
		if err != nil {
			return err
		}
		for _, want := range []string{
			fmt.Sprintf("edge %q -> %q", w.lastFrom, w.lastTo),
			fmt.Sprintf("expression %q", w.lastWhen),
		} {
			if !strings.Contains(platform.Message, want) {
				return fmt.Errorf("message %q does not contain %q", platform.Message, want)
			}
		}
		return nil
	})

	sc.Step(`^compilation fails with code "([^"]*)" naming node "([^"]*)"$`,
		func(ctx context.Context, code, name string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			if want := fmt.Sprintf("node %q", name); !strings.Contains(platform.Message, want) {
				return fmt.Errorf("message %q does not contain %q", platform.Message, want)
			}
			return nil
		})

	sc.Step(`^compilation fails with code "([^"]*)" naming the edge from "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, code, from, to string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			if want := fmt.Sprintf("edge %q -> %q", from, to); !strings.Contains(platform.Message, want) {
				return fmt.Errorf("message %q does not contain %q", platform.Message, want)
			}
			return nil
		})

	sc.Step(`^compilation fails with code "([^"]*)" naming the map node "([^"]*)" and the missing child node "([^"]*)"$`,
		func(ctx context.Context, code, mapNode, child string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			for _, want := range []string{fmt.Sprintf("map node %q", mapNode), fmt.Sprintf("child node %q", child), "does not exist"} {
				if !strings.Contains(platform.Message, want) {
					return fmt.Errorf("message %q does not contain %q", platform.Message, want)
				}
			}
			return nil
		})

	sc.Step(`^compilation fails with code "([^"]*)" naming the map node "([^"]*)", the child node "([^"]*)" and the kind "([^"]*)"$`,
		func(ctx context.Context, code, mapNode, child, kind string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			for _, want := range []string{
				fmt.Sprintf("map node %q", mapNode),
				fmt.Sprintf("child node %q", child),
				fmt.Sprintf("kind %q", kind),
				"the machine runs itself",
			} {
				if !strings.Contains(platform.Message, want) {
					return fmt.Errorf("message %q does not contain %q", platform.Message, want)
				}
			}
			return nil
		})

	sc.Step(`^compilation fails with code "([^"]*)" naming the map nodes "([^"]*)" and "([^"]*)" and the child node "([^"]*)"$`,
		func(ctx context.Context, code, first, second, child string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			for _, want := range []string{
				fmt.Sprintf("map nodes %q and %q", first, second),
				fmt.Sprintf("child node %q", child),
			} {
				if !strings.Contains(platform.Message, want) {
					return fmt.Errorf("message %q does not contain %q", platform.Message, want)
				}
			}
			return nil
		})

	sc.Step(`^compilation fails with code "([^"]*)" naming the map node "([^"]*)" and the child node "([^"]*)"$`,
		func(ctx context.Context, code, mapNode, child string) error {
			return assertMapChildFailure(world(ctx).Flow, code, mapNode, child)
		})

	sc.Step(`^compilation fails naming the map node "([^"]*)" and the child node "([^"]*)"$`,
		func(ctx context.Context, mapNode, child string) error {
			return assertMapChildFailure(world(ctx).Flow, string(core.CodeInvalidArgument), mapNode, child)
		})

	sc.Step(`^the failure says that a guard counts visits of the nodes the machine routes to while a map child runs once per element$`,
		func(ctx context.Context) error {
			w := world(ctx).Flow
			if w.lastErr == nil {
				return fmt.Errorf("compilation succeeded, want a failure")
			}
			for _, want := range []string{"a guard counts visits", "a map child runs once per element"} {
				if !strings.Contains(w.lastErr.Error(), want) {
					return fmt.Errorf("message %q does not contain %q", w.lastErr.Error(), want)
				}
			}
			return nil
		})

	sc.Step(`^compilation fails with code "([^"]*)" stating that the flow needs exactly one edge from "START"$`,
		func(ctx context.Context, code string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			if want := fmt.Sprintf("exactly one edge from %q", ir.Start); !strings.Contains(platform.Message, want) {
				return fmt.Errorf("message %q does not contain %q", platform.Message, want)
			}
			return nil
		})
}
