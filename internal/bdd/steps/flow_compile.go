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
