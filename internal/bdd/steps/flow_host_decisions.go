package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

// registerFlowHostDecisions wires scenario steps for host run outcomes. Steps that
// check failure code, message or absent text (including FLOW-057-01) assert the
// error Host.Run returned, not the node_failed event payload passed to the
// machine; that payload is not observable through DurableContext and is pinned by
// flow/host/run_handlers_test.go TestNodeFailedEventPayloadIsOnlyCodeAndMessage.
func registerFlowHostDecisions(sc *godog.ScenarioContext) {
	// FLOW-051 (REQ-FLOW-051): default node input.
	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" has no input mapping and the node spec (\{.*\})$`,
		func(ctx context.Context, node, kind, spec string) error {
			w := world(ctx).Host
			w.newFlow("tool-in", 1)
			w.addNode(node, kind, spec, "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^a flow that runs node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\}) and node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\}) in parallel, then node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\})$`,
		func(ctx context.Context, a, aKind, aSpec, b, bKind, bSpec, after, afterKind, afterSpec string) error {
			w := world(ctx).Host
			w.newFlow("parallel-fail", 1)
			w.addNode("fan", "parallel", "", "", "")
			w.addNode(a, aKind, aSpec, "", "")
			w.addNode(b, bKind, bSpec, "", "")
			w.addNode(after, afterKind, afterSpec, "", "")
			w.addEdge(ir.Start, "fan", "")
			w.addEdge("fan", a, "")
			w.addEdge("fan", b, "")
			w.addEdge(a, after, "")
			w.addEdge(b, after, "")
			return nil
		})

	sc.Step(`^a flow that runs node "([^"]*)" of kind "([^"]*)" and node "([^"]*)" of kind "([^"]*)" in parallel$`,
		func(ctx context.Context, first, firstKind, second, secondKind string) error {
			w := world(ctx).Host
			w.newFlow("handlers", 1)
			w.addNode("fan", "parallel", "", "", "")
			w.addNode(first, firstKind, "", "", "")
			w.addNode(second, secondKind, "", "", "")
			w.addEdge(ir.Start, "fan", "")
			w.addEdge("fan", first, "")
			w.addEdge("fan", second, "")
			w.registerHandledKind(firstKind)
			w.registerHandledKind(secondKind)
			w.behavior(first)
			w.behavior(second)
			return nil
		})

	sc.Step(`^Select reports the failed step "([^"]*)" before the failed step "([^"]*)"$`,
		func(ctx context.Context, prefer, _ string) error {
			world(ctx).Host.dc.selectPreferStep = prefer
			return nil
		})

	sc.Step(`^the run awaited the failed step "([^"]*)" but not the failed step "([^"]*)"$`,
		func(ctx context.Context, want, not string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			w.dc.mu.Lock()
			gets := append([]string(nil), w.dc.awaitedGets...)
			w.dc.mu.Unlock()
			awaited := func(name string) bool {
				for _, g := range gets {
					if g == name {
						return true
					}
				}
				return false
			}
			if !awaited(want) {
				return fmt.Errorf("run did not await step %q (got %+v)", want, gets)
			}
			if awaited(not) {
				return fmt.Errorf("run awaited step %q, want it skipped: %+v", not, gets)
			}
			return nil
		})

	sc.Step(`^the run made no step named "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		for _, call := range w.dc.callLog() {
			if call.kind == "step" && call.name == name {
				return fmt.Errorf("run made step %q", name)
			}
		}
		return nil
	})

	sc.Step(`^flow "([^"]*)" is registered in the definition catalog with content hash "([^"]*)"$`,
		func(ctx context.Context, name, hash string) error {
			w := world(ctx).Host
			def := w.newFlow(name, 1)
			def.Nodes["done"] = flow.Node{Kind: "set", Spec: json.RawMessage(`{"v":1}`)}
			def.Edges = []flow.Edge{{From: ir.Start, To: "done"}}
			w.register(def)
			w.catalog.registerContentAlias(hash, def)
			return nil
		})

	sc.Step(`^the catalog returns flow "([^"]*)" with content hash "([^"]*)" for a run pinned to "([^"]*)"$`,
		func(ctx context.Context, name, returnHash, pinned string) error {
			w := world(ctx).Host
			alt := &flow.IR{
				Name:    name,
				Version: 99,
				State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
				Nodes: map[string]flow.Node{
					"done": {Kind: "set", Spec: json.RawMessage(`{"v":2}`)},
				},
				Edges: []flow.Edge{{From: ir.Start, To: "done"}},
			}
			w.catalog.register(alt)
			w.catalog.registerContentAlias(returnHash, alt)
			w.catalog.setPinSubstitute(pinned, alt)
			return nil
		})

	sc.Step(`^a run of "([^"]*)" pinned to "([^"]*)" starts with input (\{.*\})$`,
		func(ctx context.Context, name, pin, input string) error {
			ref, err := world(ctx).Host.catalog.pinnedRef(name, pin)
			if err != nil {
				return err
			}
			return world(ctx).Host.startRef(ref, input)
		})

	sc.Step(`^the run fails with code "([^"]*)" naming flow "([^"]*)"$`, func(ctx context.Context, code, name string) error {
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
		var pe *core.Error
		if errors.As(w.err, &pe) {
			if f, _ := pe.Details["flow"].(string); f != "" && f != name {
				return fmt.Errorf("error names flow %q, want %q", f, name)
			}
		}
		if !strings.Contains(w.err.Error(), fmt.Sprintf("%q", name)) &&
			(pe == nil || pe.Details["flow"] == nil) {
			return fmt.Errorf("error %w does not name flow %q", w.err, name)
		}
		return nil
	})

	sc.Step(`^flow "([^"]*)" version (\d+) whose node "([^"]*)" of kind "([^"]*)" sets "([^"]*)" to (\d+) is registered in the definition catalog$`,
		func(ctx context.Context, name string, version int, node, kind, key string, value int) error {
			w := world(ctx).Host
			def := w.newFlow(name, version)
			def.Nodes[node] = flow.Node{Kind: kind, Spec: json.RawMessage(setSpec(key, value))}
			def.Edges = []flow.Edge{{From: ir.Start, To: node}}
			w.register(def)
			return nil
		})

	sc.Step(`^a run of "([^"]*)" starts with an empty pinned hash and input (\{.*\})$`,
		func(ctx context.Context, name, input string) error {
			return world(ctx).Host.startRef(runtime.FlowRef{Name: name, Hash: ""}, input)
		})

	sc.Step(`^the failure payload has code "([^"]*)" and message 'flow: node "([^"]*)" failed'$`,
		func(ctx context.Context, code, node string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			if w.err == nil {
				return fmt.Errorf("run succeeded, want failure")
			}
			wantMsg := fmt.Sprintf(`flow: node "%s" failed`, node)
			var pe *core.Error
			if errors.As(w.err, &pe) {
				if string(pe.Code) != code {
					return fmt.Errorf("code %s, want %s", pe.Code, code)
				}
				if pe.Message != wantMsg {
					return fmt.Errorf("message %q, want %q", pe.Message, wantMsg)
				}
				return nil
			}
			if string(core.CodeOf(w.err)) != code || !strings.Contains(w.err.Error(), wantMsg) {
				return fmt.Errorf("error %w, want code %s message %q", w.err, code, wantMsg)
			}
			return nil
		})

	sc.Step(`^the failure payload does not contain "([^"]*)"$`, func(ctx context.Context, forbidden string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err == nil {
			return fmt.Errorf("run succeeded, want failure")
		}
		if strings.Contains(hostRunErrorBlob(w.err), forbidden) {
			return fmt.Errorf("failure payload contains forbidden text %q", forbidden)
		}
		return nil
	})

	sc.Step(`^a flow that visits node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\}) twice in a loop guarded by at most (\d+) visits$`,
		func(ctx context.Context, node, kind, spec string, visits int) error {
			w := world(ctx).Host
			w.newFlow("loop", 1)
			w.addNode(node, kind, spec, "", "")
			def := w.ensureFlow()
			n := def.Nodes[node]
			n.Guard = &flow.Guard{MaxVisits: visits, While: `true`}
			def.Nodes[node] = n
			w.addEdge(ir.Start, node, "")
			w.addEdge(node, node, "")
			return nil
		})

	sc.Step(`^the step "([^"]*)" was called twice$`, func(ctx context.Context, name string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		n := 0
		for _, call := range w.dc.callLog() {
			if call.kind == "step" && call.name == name {
				n++
			}
		}
		if n != 2 {
			return fmt.Errorf("step %q called %d times, want 2", name, n)
		}
		return nil
	})

	sc.Step(`^the two idempotency keys of step "([^"]*)" differ only in their superstep sequence$`,
		func(ctx context.Context, step string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			var keys []parsedIdempotencyKey
			for _, call := range w.dc.callLog() {
				if call.kind != "step" || call.name != step {
					continue
				}
				parsed, err := parseHostIdempotencyKey(call.stepOpts.IdempotencyKey)
				if err != nil {
					return err
				}
				keys = append(keys, parsed)
			}
			if len(keys) != 2 {
				return fmt.Errorf("step %q has %d keys, want 2", step, len(keys))
			}
			a, b := keys[0], keys[1]
			if a.flow != b.flow || a.hash != b.hash || a.node != b.node || a.attempt != b.attempt {
				return fmt.Errorf("keys differ outside superstep sequence: %+v vs %+v", a, b)
			}
			if a.seq == b.seq {
				return fmt.Errorf("superstep sequences match: %d", a.seq)
			}
			return nil
		})

	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\}) is recorded with step "([^"]*)" failing once then returning (\{.*\})$`,
		func(ctx context.Context, node, kind, spec, step, result string) error {
			w := world(ctx).Host
			w.newFlow("replay", 1)
			w.addNode(node, kind, spec, "", "")
			w.addEdge(ir.Start, node, "")
			w.dc.steps[step] = hostScript{
				result: json.RawMessage(result),
				err:    core.NewError(core.CodeUnavailable, "step failed", nil),
			}
			if err := w.start(`{}`); err != nil {
				return err
			}
			if err := w.waitDone(); err != nil {
				return err
			}
			if w.err == nil {
				return fmt.Errorf("recording run succeeded, want a failed step %q", step)
			}
			w.firstJournal = w.dc.copyJournal()
			w.replayAcceptsFailure = true
			w.done = nil
			w.result = nil
			w.err = nil
			w.dc = newHostFake()
			w.dc.steps[step] = hostScript{result: json.RawMessage(result)}
			return nil
		})

	sc.Step(`^the replayed step "([^"]*)" carried the same idempotency key as the failed first attempt$`,
		func(ctx context.Context, step string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			firstKey, err := firstStepIdempotencyKey(w.firstJournal, step)
			if err != nil {
				return err
			}
			replayKey, err := stepIdempotencyKeyFromCallLog(w.dc.callLog(), step)
			if err != nil {
				return err
			}
			if err := w.dc.requireJournalFullyConsumed(); err != nil {
				return err
			}
			if firstKey != replayKey {
				return fmt.Errorf("replay key %q, want %q from failed attempt", replayKey, firstKey)
			}
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" was never called$`, func(ctx context.Context, node string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		w.mu.Lock()
		calls := append([]hostHandlerCall(nil), w.handlerCalls...)
		w.mu.Unlock()
		for _, call := range calls {
			if call.node == node {
				return fmt.Errorf("node handler of %q was called", node)
			}
		}
		return nil
	})
}

type parsedIdempotencyKey struct {
	flow, hash, node string
	attempt, seq     int
}

func parseHostIdempotencyKey(key string) (parsedIdempotencyKey, error) {
	lastAt := strings.LastIndex(key, "@")
	if lastAt < 0 {
		return parsedIdempotencyKey{}, fmt.Errorf("idempotency key %q: missing superstep sequence", key)
	}
	seq, err := strconv.Atoi(key[lastAt+1:])
	if err != nil {
		return parsedIdempotencyKey{}, fmt.Errorf("idempotency key %q: sequence: %w", key, err)
	}
	prefix := key[:lastAt]
	hashIdx := strings.Index(prefix, "@")
	if hashIdx < 0 {
		return parsedIdempotencyKey{}, fmt.Errorf("idempotency key %q: missing name/hash separator", key)
	}
	flowName := prefix[:hashIdx]
	rest := prefix[hashIdx+1:]
	hashSlash := strings.Index(rest, "/")
	if hashSlash < 0 {
		return parsedIdempotencyKey{}, fmt.Errorf("idempotency key %q: missing node separator", key)
	}
	hash := rest[:hashSlash]
	nodeAttempt := rest[hashSlash+1:]
	hashIdx = strings.LastIndex(nodeAttempt, "#")
	if hashIdx < 0 {
		return parsedIdempotencyKey{}, fmt.Errorf("idempotency key %q: missing attempt", key)
	}
	node := nodeAttempt[:hashIdx]
	attempt, err := strconv.Atoi(nodeAttempt[hashIdx+1:])
	if err != nil {
		return parsedIdempotencyKey{}, fmt.Errorf("idempotency key %q: attempt: %w", key, err)
	}
	return parsedIdempotencyKey{
		flow: flowName, hash: hash, node: node, attempt: attempt, seq: seq,
	}, nil
}

func firstStepIdempotencyKey(journal []hostJournalEntry, step string) (string, error) {
	for _, e := range journal {
		if e.kind == "step" && e.name == step {
			return e.stepOpts.IdempotencyKey, nil
		}
	}
	return "", fmt.Errorf("journal has no step %q", step)
}

func stepIdempotencyKeyFromCallLog(calls []hostCall, step string) (string, error) {
	for _, c := range calls {
		if c.kind == "step" && c.name == step {
			if c.stepOpts.IdempotencyKey == "" {
				return "", fmt.Errorf("step %q call has no idempotency key", step)
			}
			return c.stepOpts.IdempotencyKey, nil
		}
	}
	return "", fmt.Errorf("call log has no step %q", step)
}

func hostRunErrorBlob(err error) string {
	var parts []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		parts = append(parts, e.Error())
		var pe *core.Error
		if errors.As(e, &pe) && len(pe.Details) > 0 {
			raw, _ := json.Marshal(pe.Details)
			parts = append(parts, string(raw))
		}
	}
	return strings.Join(parts, " ")
}
