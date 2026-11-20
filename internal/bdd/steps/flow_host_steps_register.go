package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

// --- step definitions ------------------------------------------------------

func registerFlowHostSteps(sc *godog.ScenarioContext) {
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
		if _, ok := w.nodeResult(node); ok {
			return fmt.Errorf("node %q has a result, want none", node)
		}
		return nil
	})

	sc.Step(`^the run completed node "([^"]*)" with the result (\{.*\})$`, func(ctx context.Context, node, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		got, ok := w.nodeResult(node)
		if !ok {
			return fmt.Errorf("node %q has no recorded result", node)
		}
		if !jsonEquivalent(got, json.RawMessage(want)) {
			return fmt.Errorf("node %q result %s, want %s", node, got, want)
		}
		return nil
	})

	// FLOW-032: gates.
	sc.Step(`^a flow whose node "([^"]*)" of kind "gate" has the node spec (\{.*\})$`,
		func(ctx context.Context, node, spec string) error {
			w := world(ctx).Host
			w.newFlow("gate", 1)
			w.addNode(node, "gate", spec, "", node)
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the gate "([^"]*)" is resolved with (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		w := world(ctx).Host
		w.dc.resolveGate(gate, json.RawMessage(payload))
		return w.settle()
	})

	sc.Step(`^the run waited for gate "([^"]*)" with schema "([^"]*)", acceptance condition "([^"]*)" and timeout (\S+)$`,
		func(ctx context.Context, gate, schema, accept, timeout string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			gates := w.gates()
			if len(gates) != 1 {
				return fmt.Errorf("gate waits %+v, want one", gates)
			}
			got := gates[0].gate
			duration, err := time.ParseDuration(timeout)
			if err != nil {
				return err
			}
			if got.Name != gate || rawText(got.Schema) != schema || got.AcceptIf != accept || got.Timeout != duration {
				return fmt.Errorf("gate wait %+v, want %s schema %s accept %s timeout %s", got, gate, schema, accept, timeout)
			}
			return nil
		})

	// FLOW-033: subflows.
	sc.Step(`^a flow whose node "([^"]*)" of kind "subflow" has the node spec (\{(?:[^{}]|\{[^{}]*\})*\})$`,
		func(ctx context.Context, node, spec string) error {
			w := world(ctx).Host
			w.newFlow("subflow", 1)
			w.addNode(node, "subflow", spec, "", node)
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the child run "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		world(ctx).Host.dc.steps["child:"+name] = hostScript{result: json.RawMessage(result)}
		return nil
	})

	sc.Step(`^the child run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, code string) error {
		world(ctx).Host.dc.steps["child:"+name] = hostScript{err: core.NewError(core.Code(code), "child failed", nil)}
		return nil
	})

	sc.Step(`^the run started the child run "([^"]*)" with input (\{.*\})$`, func(ctx context.Context, name, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		for _, call := range w.dc.callLog() {
			if call.kind != "child" || call.child.Name != name {
				continue
			}
			if !jsonEquivalent(call.input, json.RawMessage(want)) {
				return fmt.Errorf("child input %s, want %s", call.input, want)
			}
			return nil
		}
		return fmt.Errorf("run did not start child %q", name)
	})

	// FLOW-035: failures.
	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" has a registered node handler and precedes node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\})$`,
		func(ctx context.Context, first, firstKind, second, secondKind, spec string) error {
			w := world(ctx).Host
			w.newFlow("failing", 1)
			w.addNode(first, firstKind, "", "", "")
			w.addNode(second, secondKind, spec, "", "")
			w.addEdge(ir.Start, first, "")
			w.addEdge(first, second, "")
			w.registerHandledKind(firstKind)
			w.behavior(first)
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" returns the error "([^"]*)"$`, func(ctx context.Context, node, code string) error {
		world(ctx).Host.handlerFails(node, core.NewError(core.Code(code), "handler failed", nil))
		return nil
	})

	sc.Step(`^the node handler of "([^"]*)" returns an unclassified error "([^"]*)"$`, func(ctx context.Context, node, message string) error {
		world(ctx).Host.handlerFails(node, errors.New(message))
		return nil
	})

	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" has the node spec (\{.*\}) and precedes node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\})$`,
		func(ctx context.Context, first, firstKind, firstSpec, second, secondKind, secondSpec string) error {
			w := world(ctx).Host
			w.newFlow("failing", 1)
			w.addNode(first, firstKind, firstSpec, "", "")
			w.addNode(second, secondKind, secondSpec, "", "")
			w.addEdge(ir.Start, first, "")
			w.addEdge(first, second, "")
			return nil
		})

	sc.Step(`^the run made no step$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if steps := w.steps(); len(steps) > 0 {
			return fmt.Errorf("run made steps: %+v", steps)
		}
		return nil
	})

	sc.Step(`^(\d+) (second|seconds|minute|minutes|hour|hours) pass without the gate "[^"]*" being resolved$`,
		func(ctx context.Context, count int, unit string) error {
			w := world(ctx).Host
			var duration time.Duration
			switch strings.TrimSuffix(unit, "s") {
			case "second":
				duration = time.Duration(count) * time.Second
			case "minute":
				duration = time.Duration(count) * time.Minute
			case "hour":
				duration = time.Duration(count) * time.Hour
			}
			w.dc.advance(duration)
			return w.settle()
		})

	// FLOW-036: pure nodes.
	sc.Step(`^a flow routing node "([^"]*)" of kind "set" to node "([^"]*)" of kind "set" setting "([^"]*)" to "([^"]*)" when "([^"]*)", otherwise to node "([^"]*)" of kind "set" setting "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, from, first, firstKey, firstValue, when, second, secondKey, secondValue string) error {
			w := world(ctx).Host
			w.newFlow("routing", 1)
			w.stateSchema(hostObjectSchema(map[string]string{
				"n":       `{"type":"integer"}`,
				firstKey:  `{"type":"string"}`,
				secondKey: `{"type":"string"}`,
			}))
			w.addNode(from, "set", "", "", "")
			w.addNode(first, "set", setSpec(firstKey, firstValue), "", "")
			w.addNode(second, "set", setSpec(secondKey, secondValue), "", "")
			w.addEdge(ir.Start, from, "")
			w.addEdge(from, first, when)
			w.addEdge(from, second, "")
			return nil
		})

	sc.Step(`^the run made no durable call after it completed$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		// The host returns as soon as the run completes; the run goroutine has
		// ended, so nothing may follow.
		if calls := w.dc.callLog(); len(calls) != w.callsAtDone {
			return fmt.Errorf("run made %d durable calls after it completed", len(calls)-w.callsAtDone)
		}
		return nil
	})

	sc.Step(`^a first run recorded tool "([^"]*)" failing with code "([^"]*)" (\d+) times then returning (\{.*\}), the (\S+) timer of "([^"]*)" firing, and step "([^"]*)" returning (\{.*\})$`,
		func(ctx context.Context, tool, code string, attempts int, toolResult, duration, timerNode, step, stepResult string) error {
			w := world(ctx).Host
			w.dc.instant = false
			w.dc.steps[tool] = hostScript{
				result:    json.RawMessage(toolResult),
				failTimes: attempts,
			}
			w.dc.steps[step] = hostScript{result: json.RawMessage(stepResult)}
			if err := w.start(`{}`); err != nil {
				return err
			}
			waitFor, err := time.ParseDuration(duration)
			if err != nil {
				return err
			}
			if timerNode == "b" {
				w.dc.advance(waitFor)
				if err := w.settle(); err != nil {
					return err
				}
			}
			if err := w.waitDone(); err != nil {
				return err
			}
			if w.err != nil {
				return fmt.Errorf("first run failed: %w", w.err)
			}
			if w.dc.executed[tool] != attempts+1 {
				return fmt.Errorf("tool %q ran %d times, want %d", tool, w.dc.executed[tool], attempts+1)
			}
			w.firstJournal = w.dc.copyJournal()
			_ = code
			return nil
		})

	sc.Step(`^the run is replayed from its recorded history$`, func(ctx context.Context) error {
		w := world(ctx).Host
		replay := replayHostFake(w.firstJournal)
		w.dc = replay
		w.done = nil
		if err := w.start(`{}`); err != nil {
			return err
		}
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil && !w.replayAcceptsFailure {
			return fmt.Errorf("replayed run failed: %w", w.err)
		}
		if w.dc.diverged != nil {
			return fmt.Errorf("replay diverged: %w", w.dc.diverged)
		}
		if err := w.dc.requireJournalFullyConsumed(); err != nil {
			return err
		}
		return nil
	})

	sc.Step(`^the replayed run made the same durable calls in the same order as the first run$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if err := durableReplayCallsMatch(w.firstJournal, w.dc); err != nil {
			return err
		}
		if err := w.dc.requireJournalFullyConsumed(); err != nil {
			return err
		}
		return nil
	})

	sc.Step(`^the replayed run completed node "([^"]*)" with the result (\{.*\})$`, func(ctx context.Context, node, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("replayed run failed: %w", w.err)
		}
		got, ok, err := w.runStateNodeResult(node)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("replayed node %q has no result in the replay run state", node)
		}
		if !jsonEquivalent(got, json.RawMessage(want)) {
			return fmt.Errorf("replayed node %q result %s, want %s", node, got, want)
		}
		return nil
	})

	sc.Step(`^step "([^"]*)" was not re-executed during replay$`, func(ctx context.Context, step string) error {
		w := world(ctx).Host
		if count := w.dc.executeCount(step); count != 0 {
			return fmt.Errorf("step %q ran %d times in replay, want the recorded result", step, count)
		}
		if w.dc.diverged != nil {
			return fmt.Errorf("replay diverged: %w", w.dc.diverged)
		}
		return nil
	})

	// FLOW-038: a set node completes the run.
	sc.Step(`^a flow whose node "([^"]*)" of kind "set" sets "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, node, key, value string) error {
			w := world(ctx).Host
			w.newFlow("set", 1)
			w.stateSchema(hostObjectSchema(map[string]string{key: `{"type":"string"}`}))
			w.addNode(node, "set", setSpec(key, value), "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	// FLOW-044: parallel timers.
	sc.Step(`^a flow that runs node "([^"]*)" of kind "wait" for "([^"]*)" and node "([^"]*)" of kind "wait" for "([^"]*)" in parallel$`,
		func(ctx context.Context, first, firstDuration, second, secondDuration string) error {
			w := world(ctx).Host
			w.newFlow("parallel", 1)
			w.addNode("fan", "parallel", "", "", "")
			w.addNode(first, "wait", fmt.Sprintf(`{"duration":%q}`, firstDuration), "", "")
			w.addNode(second, "wait", fmt.Sprintf(`{"duration":%q}`, secondDuration), "", "")
			w.addEdge(ir.Start, "fan", "")
			w.addEdge("fan", first, "")
			w.addEdge("fan", second, "")
			return nil
		})

}

func (w *hostWorld) steps() []hostCall {
	var out []hostCall
	for _, call := range w.dc.callLog() {
		if call.kind == "step" {
			out = append(out, call)
		}
	}
	return out
}

func (w *hostWorld) timers() []hostCall {
	var out []hostCall
	for _, call := range w.dc.callLog() {
		if call.kind == "timer" {
			out = append(out, call)
		}
	}
	return out
}

func (w *hostWorld) gates() []hostCall {
	var out []hostCall
	for _, call := range w.dc.callLog() {
		if call.kind == "gate" {
			out = append(out, call)
		}
	}
	return out
}

// runStateNodeResult is the replayed-run outcome from Host.Run final state only (no handler bookkeeping).
func (w *hostWorld) runStateNodeResult(node string) (json.RawMessage, bool, error) {
	if w.flow == nil {
		return nil, false, nil
	}
	n, ok := w.flow.Nodes[node]
	if !ok || n.Out == "" {
		return nil, false, fmt.Errorf("flow host test: node %q has no Out key for run-state result", node)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(w.result, &state); err != nil {
		return nil, false, fmt.Errorf("replayed run state %s: %w", w.result, err)
	}
	value, ok := state[n.Out]
	if !ok {
		return nil, false, nil
	}
	return value, true, nil
}

// nodeResult reads the result a node completed with: the state key it writes,
// the gate payload, the child run's result or a handler's output.
func (w *hostWorld) nodeResult(node string) (json.RawMessage, bool) {
	if w.flow != nil {
		if n, ok := w.flow.Nodes[node]; ok {
			if n.Out != "" {
				var state map[string]json.RawMessage
				if err := json.Unmarshal(w.result, &state); err == nil {
					if value, ok := state[n.Out]; ok {
						return value, true
					}
				}
			}
			if n.Kind == "subflow" {
				var spec struct {
					Flow string `json:"flow"`
				}
				if err := json.Unmarshal(n.Spec, &spec); err == nil {
					w.dc.mu.Lock()
					payload, ok := w.dc.childDone[spec.Flow]
					w.dc.mu.Unlock()
					if ok {
						return payload, true
					}
				}
			}
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if b := w.behaviors[node]; b != nil && len(b.result) > 0 {
		return b.result, true
	}
	if b := w.behaviors[node]; b != nil && len(b.output) > 0 && b.step == "" {
		return b.output, true
	}
	return nil, false
}

func hasEdgeFrom(def *flow.IR, from, to string) bool {
	for _, edge := range def.Edges {
		if edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}

func durableReplayCallsMatch(first []hostJournalEntry, replay *hostFake) error {
	want := journalDurableCalls(first)
	got := replay.callLog()
	if len(want) != len(got) {
		return fmt.Errorf("replay durable calls %d, want %d from first run", len(got), len(want))
	}
	for i := range want {
		if err := hostCallsEqual(want[i], got[i]); err != nil {
			return fmt.Errorf("durable call %d: %w", i, err)
		}
	}
	var wantIDs []hostJournalEntry
	for _, e := range first {
		if e.kind == "id" {
			wantIDs = append(wantIDs, e)
		}
	}
	replay.mu.Lock()
	var gotIDs int
	for _, e := range replay.journal {
		if e.kind == "id" {
			if gotIDs >= len(wantIDs) {
				break
			}
			if e.idKind != wantIDs[gotIDs].idKind || e.id != wantIDs[gotIDs].id {
				replay.mu.Unlock()
				return fmt.Errorf("replay id %d %q-%q, want %q-%q", gotIDs, e.idKind, e.id, wantIDs[gotIDs].idKind, wantIDs[gotIDs].id)
			}
			gotIDs++
		}
	}
	replay.mu.Unlock()
	if gotIDs != len(wantIDs) {
		return fmt.Errorf("replay consumed %d ids, want %d", gotIDs, len(wantIDs))
	}
	return nil
}

func journalDurableCalls(journal []hostJournalEntry) []hostCall {
	var out []hostCall
	for _, e := range journal {
		if e.kind == "id" {
			continue
		}
		out = append(out, hostCall{
			kind: e.kind, name: e.name, input: e.input, duration: e.duration,
			gate: e.gate, child: e.child, stepOpts: e.stepOpts, childOpts: e.childOpts,
		})
	}
	return out
}

func hostCallsEqual(a, b hostCall) error {
	if a.kind != b.kind || a.name != b.name || a.duration != b.duration {
		return fmt.Errorf("got %+v, want %+v", b, a)
	}
	if a.child.Name != b.child.Name || a.child.Hash != b.child.Hash {
		return fmt.Errorf("child %+v, want %+v", b.child, a.child)
	}
	if !rawJSONEqual(a.input, b.input) {
		return fmt.Errorf("input mismatch")
	}
	if a.stepOpts.IdempotencyKey != b.stepOpts.IdempotencyKey || a.childOpts.ID != b.childOpts.ID {
		return fmt.Errorf("keys mismatch")
	}
	if a.gate.Name != b.gate.Name || a.gate.Timeout != b.gate.Timeout || a.gate.AcceptIf != b.gate.AcceptIf ||
		!bytes.Equal(a.gate.Schema, b.gate.Schema) {
		return fmt.Errorf("gate mismatch")
	}
	return nil
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func rawJSONEqual(left, right json.RawMessage) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	return jsonEquivalent(left, right)
}

func jsonEquivalent(left, right json.RawMessage) bool {
	var a, b any
	if err := decodeJSONValue(left, &a); err != nil {
		return false
	}
	if err := decodeJSONValue(right, &b); err != nil {
		return false
	}
	al, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bl, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(al, bl)
}

func decodeJSONValue(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(out)
}
