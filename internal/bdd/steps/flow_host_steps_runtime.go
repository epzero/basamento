package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow/ir"
)

func registerFlowHostStepRuntime(sc *godog.ScenarioContext) {
	// FLOW-032 (REQ-FLOW-032): gates.
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

	// FLOW-033 (REQ-FLOW-033): subflows.
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

	// FLOW-035 (REQ-FLOW-035): failures.
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

	// FLOW-036 (REQ-FLOW-036): pure nodes.
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

	// FLOW-038 (REQ-FLOW-038): a set node completes the run.
	sc.Step(`^a flow whose node "([^"]*)" of kind "set" sets "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, node, key, value string) error {
			w := world(ctx).Host
			w.newFlow("set", 1)
			w.stateSchema(hostObjectSchema(map[string]string{key: `{"type":"string"}`}))
			w.addNode(node, "set", setSpec(key, value), "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	// FLOW-044 (REQ-FLOW-044): parallel timers.
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
