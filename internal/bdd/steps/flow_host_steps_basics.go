package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func registerFlowHostStepBasics(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		world(ctx).Host.abortInFlightRun()
		return ctx, nil
	})

	sc.Step(`^a recording fake durable context$`, func(ctx context.Context) error {
		w := world(ctx).Host
		w.dc = newHostFake()
		return nil
	})

	// FLOW-027-01: the pinned definition.
	sc.Step(`^flow "([^"]*)" version (\d+) whose node "([^"]*)" of kind "([^"]*)" with output "([^"]*)" has a registered node handler and precedes node "([^"]*)" of kind "set" setting "([^"]*)" to (-?\d+)$`,
		func(ctx context.Context, name string, version int, first, kind, out, second, key string, value int) error {
			w := world(ctx).Host
			def := w.newFlow(name, version)
			def.State.Schema = json.RawMessage(hostObjectSchema(map[string]string{
				"plan": `{"type":"object"}`,
				key:    `{"type":"integer"}`,
			}))
			def.Nodes[first] = flow.Node{Kind: kind, Out: out}
			def.Nodes[second] = flow.Node{Kind: "set", Spec: json.RawMessage(setSpec(key, value))}
			def.Edges = []flow.Edge{{From: ir.Start, To: first}, {From: first, To: second}}
			w.ensureHandler(kind)
			b := w.behavior(first)
			b.blocking = true
			return nil
		})

	sc.Step(`^flow "([^"]*)" version (\d+) is registered$`, func(ctx context.Context, name string, version int) error {
		w := world(ctx).Host
		defs := w.defs[name]
		if defs == nil || defs[version] == nil {
			return fmt.Errorf("flow %q version %d is not declared", name, version)
		}
		w.register(defs[version])
		return nil
	})

	sc.Step(`^a run of "([^"]*)" version (\d+) starts and waits at the node "([^"]*)"$`,
		func(ctx context.Context, name string, version int, node string) error {
			w := world(ctx).Host
			if err := w.startVersion(name, version, `{}`); err != nil {
				return err
			}
			w.mu.Lock()
			b := w.behaviors[node]
			called := b != nil && b.called
			w.mu.Unlock()
			if !called {
				return fmt.Errorf("handler for node %q was not called", node)
			}
			return nil
		})

	sc.Step(`^flow "([^"]*)" version (\d+), where node "([^"]*)" with output "([^"]*)" precedes node "([^"]*)" of kind "set" setting "([^"]*)" to (-?\d+), is registered$`,
		func(ctx context.Context, name string, version int, first, out, second, key string, value int) error {
			w := world(ctx).Host
			def := &flow.IR{
				Name:    name,
				Version: version,
				State: flow.StateSpec{Schema: json.RawMessage(hostObjectSchema(map[string]string{
					"plan": `{"type":"object"}`,
					key:    `{"type":"integer"}`,
				}))},
				Nodes: map[string]flow.Node{
					first:  {Kind: "agent", Out: out},
					second: {Kind: "set", Spec: json.RawMessage(setSpec(key, value))},
				},
				Edges: []flow.Edge{{From: ir.Start, To: first}, {From: first, To: second}},
			}
			w.defs[name][version] = def
			w.register(def)
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, node, output string) error {
		return world(ctx).Host.handlerReturns(node, output)
	})

	sc.Step(`^the run completes with state (\{.*\})$`, func(ctx context.Context, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		if !jsonEquivalent(w.result, json.RawMessage(want)) {
			return fmt.Errorf("state %s, want %s", w.result, want)
		}
		return nil
	})

	sc.Step(`^the run completes with state ([^ ]+) = (\{.*\})$`, func(ctx context.Context, key, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(w.result, &state); err != nil {
			return fmt.Errorf("state %s: %w", w.result, err)
		}
		if !jsonEquivalent(state[key], json.RawMessage(want)) {
			return fmt.Errorf("state.%s is %s, want %s", key, state[key], want)
		}
		return nil
	})

	// FLOW-028: a kind without a handler.
	sc.Step(`^flow "([^"]*)" whose node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\}) precedes the node "([^"]*)" of kind "([^"]*)"$`,
		func(ctx context.Context, name, first, firstKind, spec, second, secondKind string) error {
			w := world(ctx).Host
			def := w.newFlow(name, 1)
			def.Nodes[first] = flow.Node{Kind: firstKind, Spec: json.RawMessage(spec)}
			def.Nodes[second] = flow.Node{Kind: secondKind}
			def.Edges = []flow.Edge{{From: ir.Start, To: first}, {From: first, To: second}}
			return nil
		})

	sc.Step(`^no node handler is registered for the kind "([^"]*)"$`, func(ctx context.Context, kind string) error {
		w := world(ctx).Host
		w.mu.Lock()
		registered := w.handlerKinds[kind]
		w.mu.Unlock()
		if registered {
			return fmt.Errorf("a node handler is registered for kind %q", kind)
		}
		return nil
	})

	sc.Step(`^a run of "([^"]*)" starts with input (\{.*\})$`, func(ctx context.Context, name, input string) error {
		w := world(ctx).Host
		defs := w.defs[name]
		if defs == nil {
			return fmt.Errorf("flow %q is not declared", name)
		}
		var def *flow.IR
		for _, candidate := range defs {
			def = candidate
		}
		return w.startRef(w.refFor(def), input)
	})

	sc.Step(`^the run fails with code "([^"]*)" naming node "([^"]*)"$`, func(ctx context.Context, code, node string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err == nil {
			return fmt.Errorf("run succeeded, want failure %q", code)
		}
		var platform *core.Error
		if !errors.As(w.err, &platform) {
			return fmt.Errorf("error is not a *core.Error: %w", w.err)
		}
		if string(platform.Code) != code {
			return fmt.Errorf("code %s, want %s: %s", platform.Code, code, platform.Message)
		}
		if got, _ := platform.Details["node"].(string); got != node {
			return fmt.Errorf("error details name node %q, want %q", got, node)
		}
		return nil
	})

	sc.Step(`^the run fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err == nil {
			return fmt.Errorf("run succeeded, want failure %q", code)
		}
		if got := string(core.CodeOf(w.err)); got != code {
			return fmt.Errorf("code %s, want %s: %w", got, code, w.err)
		}
		return nil
	})

	sc.Step(`^the run made no step, timer, gate wait, child run or durable event$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		for _, call := range w.dc.callLog() {
			switch call.kind {
			case "step", "timer", "gate", "child", "emit", "checkpoint":
				return fmt.Errorf("run made a %s call: %+v", call.kind, call)
			}
		}
		return nil
	})

	// FLOW-029/030: an effectful node with input and output mapping.
	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" maps in the input from "([^"]*)", writes output to "([^"]*)", and has the node spec (\{.*\})$`,
		func(ctx context.Context, node, kind, in, out, spec string) error {
			w := world(ctx).Host
			w.newFlow("mapped", 1)
			w.stateSchema(hostObjectSchema(map[string]string{"draft": `{"type":"object"}`, out: `{"type":"object"}`}))
			w.addNode(node, kind, spec, in, out)
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the node "([^"]*)" maps in the input from "([^"]*)", writes output to "([^"]*)", and has the node spec (\{.*\})$`,
		func(ctx context.Context, node, in, out, spec string) error {
			w := world(ctx).Host
			def := w.ensureFlow()
			n := def.Nodes[node]
			n.In, n.Out, n.Spec = in, out, json.RawMessage(spec)
			def.Nodes[node] = n
			return nil
		})

	sc.Step(`^the step "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		w := world(ctx).Host
		w.dc.steps[name] = hostScript{result: json.RawMessage(result)}
		return nil
	})

	sc.Step(`^the step "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, code string) error {
		w := world(ctx).Host
		w.dc.steps[name] = hostScript{err: core.NewError(core.Code(code), "step failed", nil)}
		return nil
	})

	sc.Step(`^a run of the flow starts with input (\{.*\})$`, func(ctx context.Context, input string) error {
		return world(ctx).Host.start(input)
	})

	sc.Step(`^the run made exactly one step, named "([^"]*)", with input (\{.*\})$`,
		func(ctx context.Context, name, want string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			steps := w.steps()
			if len(steps) != 1 {
				return fmt.Errorf("run made %d steps, want 1: %+v", len(steps), steps)
			}
			if steps[0].name != name {
				return fmt.Errorf("step name %q, want %q", steps[0].name, name)
			}
			if !jsonEquivalent(steps[0].input, json.RawMessage(want)) {
				return fmt.Errorf("step input %s, want %s", steps[0].input, want)
			}
			return nil
		})

	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" has a registered node handler$`,
		func(ctx context.Context, node, kind string) error {
			w := world(ctx).Host
			if w.flow == nil {
				w.newFlow("handled", 1)
			}
			w.addNode(node, kind, "", "", "")
			if !hasEdgeFrom(w.flow, ir.Start, node) {
				w.addEdge(ir.Start, node, "")
			}
			w.registerHandledKind(kind)
			w.behavior(node)
			return nil
		})

	sc.Step(`^a flow whose node "([^"]*)" of kind "set" precedes node "([^"]*)" of kind "agent" with a registered node handler$`,
		func(ctx context.Context, verify, repair string) error {
			w := world(ctx).Host
			def := w.newFlow("repair-loop", 1)
			def.State.Schema = json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`)
			w.addNode(verify, "set", `{"verified":true}`, "", "")
			w.addNode(repair, "agent", `{"agent":"repair"}`, "", "failed")
			w.addEdge(ir.Start, verify, "")
			w.addEdge(verify, repair, "")
			w.registerHandledKind("agent")
			w.behavior(repair)
			return nil
		})

	sc.Step(`^node "([^"]*)" has a guard of (\d+) milli-points and routes back to node "([^"]*)"$`,
		func(ctx context.Context, node string, guard int, back string) error {
			w := world(ctx).Host
			def := w.ensureFlow()
			n := def.Nodes[node]
			n.Guard = &flow.Guard{CostMpts: int64(guard)}
			def.Nodes[node] = n
			w.addEdge(node, back, "")
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" returns (\{.*\}) with a settled cost of (\d+) milli-points$`,
		func(ctx context.Context, node, output string, cost int) error {
			b := world(ctx).Host.behavior(node)
			if err := json.Unmarshal([]byte(output), &b.output); err != nil {
				return fmt.Errorf("handler output %q: %w", output, err)
			}
			b.costMpts = int64(cost)
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" was called with node "([^"]*)",? and input (\{.*\})$`,
		func(ctx context.Context, handler, node, want string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			w.mu.Lock()
			calls := append([]hostHandlerCall(nil), w.handlerCalls...)
			w.mu.Unlock()
			for _, call := range calls {
				if call.node != node {
					continue
				}
				if !jsonEquivalent(call.input, json.RawMessage(want)) {
					return fmt.Errorf("handler input %s, want %s", call.input, want)
				}
				return nil
			}
			return fmt.Errorf("node handler of %q was not called", handler)
		})

	// FLOW-031: wait nodes.
	sc.Step(`^a flow whose node "([^"]*)" of kind "wait" has the node spec (\{.*\})$`,
		func(ctx context.Context, node, spec string) error {
			w := world(ctx).Host
			w.newFlow("wait", 1)
			w.addNode(node, "wait", spec, "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the run made a timer for (\S+)$`, func(ctx context.Context, want string) error {
		w := world(ctx).Host
		duration, err := time.ParseDuration(want)
		if err != nil {
			return err
		}
		timers := w.timers()
		if len(timers) != 1 || timers[0].duration != duration {
			return fmt.Errorf("timers %+v, want one %s", timers, want)
		}
		return nil
	})

	sc.Step(`^the run made a timer for (\S+) and a timer for (\S+)$`, func(ctx context.Context, first, second string) error {
		w := world(ctx).Host
		one, err := time.ParseDuration(first)
		if err != nil {
			return err
		}
		two, err := time.ParseDuration(second)
		if err != nil {
			return err
		}
		timers := w.timers()
		if len(timers) != 2 || timers[0].duration != one || timers[1].duration != two {
			return fmt.Errorf("timers %+v, want %s and %s", timers, first, second)
		}
		return nil
	})

	sc.Step(`^the run is waiting$`, func(ctx context.Context) error {
		w := world(ctx).Host
		select {
		case <-w.done:
			return fmt.Errorf("run finished, want it waiting")
		default:
			return nil
		}
	})

	sc.Step(`^the run completed node "([^"]*)" with no result$`, func(ctx context.Context, node string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		return w.nodeExpectsNoRunStateResult(node)
	})

	sc.Step(`^the run completed node "([^"]*)" with the result (\{.*\})$`, func(ctx context.Context, node, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		got, err := w.nodeStateResult(node)
		if err != nil {
			return err
		}
		if !jsonEquivalent(got, json.RawMessage(want)) {
			return fmt.Errorf("node %q result %s, want %s", node, got, want)
		}
		return nil
	})

}
