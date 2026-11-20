package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/flow/machine"
)

// machineWorld holds the flow a machine scenario runs, its latest snapshot and
// commands.
type machineWorld struct {
	ir       *flow.IR
	machine  *machine.Machine
	snapshot flow.Snapshot
	commands []flow.Command
	lastErr  error

	repairCompletionCost int64
	runInput             json.RawMessage
}

func newMachineWorld() *machineWorld {
	return &machineWorld{}
}

func (w *machineWorld) started() bool {
	return w.machine != nil
}

func (w *machineWorld) nodeRuns(node string) (int, error) {
	if w.snapshot.Visit == nil {
		return 0, nil
	}
	return w.snapshot.Visit[node], nil
}

func (w *machineWorld) failure() (code, message string, ok bool) {
	if len(w.commands) != 1 || w.commands[0].Type != "fail" {
		return "", "", false
	}
	var payload map[string]any
	if err := json.Unmarshal(w.commands[0].Input, &payload); err != nil {
		return "", "", false
	}
	code, _ = payload["code"].(string)
	message, _ = payload["message"].(string)
	return code, message, code != ""
}

func (w *machineWorld) start(input string) error {
	m, err := machine.New(w.ir)
	if err != nil {
		return err
	}
	w.machine = m
	raw := json.RawMessage(input)
	if len(w.runInput) > 0 {
		raw = w.runInput
	}
	w.snapshot, w.commands, w.lastErr = m.Start(raw)
	return w.lastErr
}

func (w *machineWorld) driveRunUntilFail(maxSteps int) error {
	for step := 0; len(w.commands) != 1 || w.commands[0].Type != "fail"; step++ {
		if step >= maxSteps {
			return fmt.Errorf("run did not fail within %d steps; commands %+v", maxSteps, w.commands)
		}
		if len(w.commands) == 0 {
			return fmt.Errorf("no commands to apply")
		}
		cmd := w.commands[0]
		switch cmd.Type {
		case "run_node":
			ev := flow.Event{Type: "node_completed", Node: cmd.Node, Payload: json.RawMessage(`{}`)}
			if cmd.Node == "repair" && w.repairCompletionCost > 0 {
				ev.CostMpts = w.repairCompletionCost
			}
			w.snapshot, w.commands, w.lastErr = w.machine.Apply(w.snapshot, ev)
		case "fail":
			return nil
		default:
			return fmt.Errorf("unexpected command %+v while driving run", cmd)
		}
		if w.lastErr != nil {
			return w.lastErr
		}
	}
	return nil
}

func (w *machineWorld) complete(node string, payload json.RawMessage) error {
	w.snapshot, w.commands, w.lastErr = w.machine.Apply(w.snapshot, flow.Event{
		Type:    "node_completed",
		Node:    node,
		Payload: payload,
	})
	return w.lastErr
}

func (w *machineWorld) state() (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(w.snapshot.State))
	decoder.UseNumber()
	var state map[string]any
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	return state, nil
}

func playOrder(def *flow.IR, order []string) (flow.Snapshot, error) {
	m, err := machine.New(def)
	if err != nil {
		return flow.Snapshot{}, err
	}
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		return flow.Snapshot{}, err
	}
	if len(commands) == 0 {
		return flow.Snapshot{}, fmt.Errorf("flow %q started no command", def.Name)
	}
	for _, node := range order {
		payload, err := json.Marshal(node)
		if err != nil {
			return flow.Snapshot{}, err
		}
		snapshot, _, err = m.Apply(snapshot, flow.Event{
			Type:    "node_completed",
			Node:    node,
			Payload: payload,
		})
		if err != nil {
			return flow.Snapshot{}, err
		}
	}
	return snapshot, nil
}

func registerFlowMachine(sc *godog.ScenarioContext) {
	sc.Step(`^flow "([^"]*)" routes node "([^"]*)" to "([^"]*)" when "([^"]*)", to "([^"]*)" when "([^"]*)", otherwise to "([^"]*)"$`,
		func(ctx context.Context, name, from, first, firstWhen, second, secondWhen, fallback string) error {
			w := world(ctx).Machine
			w.ir = &flow.IR{
				Name:    name,
				Version: 1,
				State: flow.StateSpec{
					Schema: json.RawMessage(
						`{"type":"object","properties":{"failed":{"type":"integer"}}}`),
					Reducers: map[string]string{"failed": "replace"},
				},
				Nodes: map[string]flow.Node{
					from:     {Kind: "task", Out: "failed"},
					first:    {Kind: "task"},
					second:   {Kind: "task"},
					fallback: {Kind: "task"},
				},
				Edges: []flow.Edge{
					{From: ir.Start, To: from},
					{From: from, To: first, When: firstWhen},
					{From: from, To: second, When: secondWhen},
					{From: from, To: fallback},
				},
			}
			return w.start(`{}`)
		})

	sc.Step(`^node "([^"]*)" completes with state failed = (-?\d+)$`,
		func(ctx context.Context, node string, failed int) error {
			w := world(ctx).Machine
			return w.complete(node, json.RawMessage(strconv.Itoa(failed)))
		})

	sc.Step(`^the next node is "([^"]*)"$`, func(ctx context.Context, want string) error {
		w := world(ctx).Machine
		if w.lastErr != nil {
			return w.lastErr
		}
		if len(w.commands) != 1 {
			return fmt.Errorf("commands %+v, want exactly one", w.commands)
		}
		if w.commands[0].Type != "run_node" || w.commands[0].Node != want {
			return fmt.Errorf("next command %+v, want run_node %q", w.commands[0], want)
		}
		return nil
	})

	sc.Step(`^flow "([^"]*)" runs nodes "([^"]*)", "([^"]*)" and "([^"]*)" in parallel, each appending its name to "([^"]*)"$`,
		func(ctx context.Context, name, first, second, third, key string) error {
			w := world(ctx).Machine
			key = strings.TrimPrefix(key, "state.")
			children := []string{first, second, third}
			nodes := map[string]flow.Node{
				name: {Kind: "parallel"},
			}
			edges := []flow.Edge{{From: ir.Start, To: name}}
			for _, child := range children {
				nodes[child] = flow.Node{Kind: "task", Out: key}
				edges = append(edges, flow.Edge{From: name, To: child})
			}
			schema, err := json.Marshal(map[string]any{
				"type": "object",
				"properties": map[string]any{
					key: map[string]any{"type": "array"},
				},
			})
			if err != nil {
				return err
			}
			w.ir = &flow.IR{
				Name:    name,
				Version: 1,
				State: flow.StateSpec{
					Schema:   schema,
					Reducers: map[string]string{key: "append"},
				},
				Nodes: nodes,
				Edges: edges,
			}
			return w.start(`{}`)
		})

	sc.Step(`^the nodes complete in order "([^"]*)", "([^"]*)", "([^"]*)"$`,
		func(ctx context.Context, first, second, third string) error {
			w := world(ctx).Machine
			for _, node := range []string{first, second, third} {
				if err := w.complete(node, json.RawMessage(strconv.Quote(node))); err != nil {
					return err
				}
			}
			return nil
		})

	sc.Step(`^"state\.([^"]*)" is (\[[^\]]*\])$`, func(ctx context.Context, key, want string) error {
		w := world(ctx).Machine
		state, err := w.state()
		if err != nil {
			return err
		}
		var expected any
		if err := json.Unmarshal([]byte(want), &expected); err != nil {
			return fmt.Errorf("expected value %q: %w", want, err)
		}
		canonical, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		actual, err := json.Marshal(state[key])
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, canonical) {
			return fmt.Errorf("state.%s is %s, want %s", key, actual, canonical)
		}
		return nil
	})

	sc.Step(`^the snapshot equals the snapshot for completion order "([^"]*)", "([^"]*)", "([^"]*)"$`,
		func(ctx context.Context, first, second, third string) error {
			w := world(ctx).Machine
			if w.ir == nil {
				return fmt.Errorf("no flow declared")
			}
			other, err := playOrder(w.ir, []string{first, second, third})
			if err != nil {
				return err
			}
			left, err := json.Marshal(w.snapshot)
			if err != nil {
				return err
			}
			right, err := json.Marshal(other)
			if err != nil {
				return err
			}
			if !bytes.Equal(left, right) {
				return fmt.Errorf("snapshot %s, want %s", left, right)
			}
			return nil
		})

	sc.Step(`^a flow "([^"]*)" where node "([^"]*)" of kind "([^"]*)" precedes node "([^"]*)" of kind "([^"]*)", which routes back to node "([^"]*)" and has a guard of (\d+) visits$`,
		func(ctx context.Context, name, from, fromKind, repair, repairKind, back string, visits int) error {
			return world(ctx).Machine.declareGuardedLoop(name, from, fromKind, repair, repairKind, back, &flow.Guard{MaxVisits: visits})
		})

	sc.Step(`^a flow "([^"]*)" where node "([^"]*)" of kind "([^"]*)" precedes node "([^"]*)" of kind "([^"]*)", which routes back to node "([^"]*)" and has a guard of (\d+) milli-points$`,
		func(ctx context.Context, name, from, fromKind, repair, repairKind, back string, cost int) error {
			return world(ctx).Machine.declareGuardedLoop(name, from, fromKind, repair, repairKind, back, &flow.Guard{CostMpts: int64(cost)})
		})

	sc.Step(`^each completion of node "([^"]*)" carries a settled cost of (\d+) milli-points$`,
		func(ctx context.Context, node string, cost int64) error {
			w := world(ctx).Machine
			if node != "repair" {
				return fmt.Errorf("only repair loop scenarios set completion cost, got %q", node)
			}
			w.repairCompletionCost = cost
			return nil
		})

	sc.Step(`^a run of the flow starts$`, func(ctx context.Context) error {
		w := world(ctx).Machine
		if err := w.start(`{}`); err != nil {
			return err
		}
		if w.repairCompletionCost > 0 {
			return w.driveRunUntilFail(10)
		}
		return nil
	})

	sc.Step(`^a run of the flow has started$`, func(ctx context.Context) error {
		return world(ctx).Machine.start(`{}`)
	})

	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" precedes node "([^"]*)" of kind "([^"]*)"$`,
		func(ctx context.Context, a, aKind, b, bKind string) error {
			w := world(ctx).Machine
			w.ir = &flow.IR{
				Name:    "cost-accrue",
				Version: 1,
				State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
				Nodes: map[string]flow.Node{
					a: {Kind: aKind},
					b: {Kind: bKind},
				},
				Edges: []flow.Edge{
					{From: ir.Start, To: a},
					{From: a, To: b},
				},
			}
			return nil
		})

	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" routes to node "([^"]*)" of kind "([^"]*)" when "([^"]*)", otherwise to node "([^"]*)" of kind "([^"]*)"$`,
		func(ctx context.Context, a, aKind, b, bKind, when, c, cKind string) error {
			w := world(ctx).Machine
			w.ir = &flow.IR{
				Name:    "cost-route",
				Version: 1,
				State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
				Nodes: map[string]flow.Node{
					a: {Kind: aKind},
					b: {Kind: bKind},
					c: {Kind: cKind},
				},
				Edges: []flow.Edge{
					{From: ir.Start, To: a},
					{From: a, To: b, When: when},
					{From: a, To: c},
				},
			}
			return nil
		})

	sc.Step(`^a flow whose node "([^"]*)" of kind "map" fans out over "state\.([^"]*)" to node "([^"]*)" with "max_parallel" (\d+)$`,
		func(ctx context.Context, mapNode, stateKey, child string, maxParallel int) error {
			w := world(ctx).Machine
			schema, _ := json.Marshal(map[string]any{
				"type": "object",
				"properties": map[string]any{
					stateKey: map[string]any{"type": "array"},
				},
			})
			spec, _ := json.Marshal(map[string]any{
				"over":         "state." + stateKey,
				"node":         child,
				"max_parallel": maxParallel,
			})
			w.ir = &flow.IR{
				Name:    "map-fanout",
				Version: 1,
				State:   flow.StateSpec{Schema: schema},
				Nodes: map[string]flow.Node{
					mapNode: {Kind: "map", Spec: spec},
					child:   {Kind: "task"},
				},
				Edges: []flow.Edge{
					{From: ir.Start, To: mapNode},
					{From: mapNode, To: child},
				},
			}
			return nil
		})

	sc.Step(`^"state\.([^"]*)" holds (\d+) items$`, func(ctx context.Context, key string, n int) error {
		items := make([]int, n)
		for i := range items {
			items[i] = i + 1
		}
		payload, err := json.Marshal(map[string]any{key: items})
		if err != nil {
			return err
		}
		world(ctx).Machine.runInput = payload
		return nil
	})

	sc.Step(`^the run fails with code "([^"]+)" naming node "([^"]*)", (\d+) items and the limit (\d+)$`,
		func(ctx context.Context, code, node string, count, limit int) error {
			w := world(ctx).Machine
			gotCode, message, ok := w.failure()
			if !ok {
				return fmt.Errorf("run did not fail")
			}
			if gotCode != code {
				return fmt.Errorf("fail code %q, want %q", gotCode, code)
			}
			if !strings.Contains(message, fmt.Sprintf("%q", node)) {
				return fmt.Errorf("message %q does not name node %q", message, node)
			}
			if !strings.Contains(message, strconv.Itoa(count)) {
				return fmt.Errorf("message %q does not contain item count %d", message, count)
			}
			if !strings.Contains(message, strconv.Itoa(limit)) {
				return fmt.Errorf("message %q does not contain limit %d", message, limit)
			}
			return nil
		})

	sc.Step(`^the run created no fan-out$`, func(ctx context.Context) error {
		w := world(ctx).Machine
		for _, cmd := range w.commands {
			if cmd.Type == "run_node" {
				return fmt.Errorf("got run_node command %+v, want no fan-out", cmd)
			}
		}
		if len(w.snapshot.Steps) != 0 {
			return fmt.Errorf("snapshot has %d pending steps, want none", len(w.snapshot.Steps))
		}
		return nil
	})

	sc.Step(`^the failure carries no state data$`, func(ctx context.Context) error {
		w := world(ctx).Machine
		if len(w.commands) != 1 || w.commands[0].Type != "fail" {
			return fmt.Errorf("commands %+v, want single fail", w.commands)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(w.commands[0].Input, &payload); err != nil {
			return err
		}
		for key := range payload {
			if key != "code" && key != "message" {
				return fmt.Errorf("fail payload has key %q, want only code and message", key)
			}
		}
		return nil
	})

	sc.Step(`^node "([^"]*)" fans out to (\d+) items of node "([^"]*)"$`,
		func(ctx context.Context, mapNode string, want int, child string) error {
			w := world(ctx).Machine
			if w.commands[0].Node != mapNode && len(w.commands) > 0 {
				// fan-out is from map expansion: commands are for child
			}
			var got int
			seen := make(map[int]bool, want)
			for _, cmd := range w.commands {
				if cmd.Type != "run_node" || cmd.Node != child {
					return fmt.Errorf("command %+v, want run_node %q", cmd, child)
				}
				if cmd.Attempt < 1 || cmd.Attempt > want {
					return fmt.Errorf("attempt %d out of range 1..%d", cmd.Attempt, want)
				}
				if seen[cmd.Attempt] {
					return fmt.Errorf("duplicate attempt %d", cmd.Attempt)
				}
				seen[cmd.Attempt] = true
				got++
			}
			if got != want {
				return fmt.Errorf("%d run_node commands, want %d", got, want)
			}
			return nil
		})

	sc.Step(`^node "([^"]*)" completes with a settled cost of (\d+) milli-points$`,
		func(ctx context.Context, node string, cost int64) error {
			w := world(ctx).Machine
			if !w.started() {
				if err := w.start(`{}`); err != nil {
					return err
				}
			}
			if len(w.commands) != 1 || w.commands[0].Type != "run_node" {
				return fmt.Errorf("commands %+v, want one run_node", w.commands)
			}
			if w.commands[0].Node != node {
				return fmt.Errorf("next node %q, want %q", w.commands[0].Node, node)
			}
			w.snapshot, w.commands, w.lastErr = w.machine.Apply(w.snapshot, flow.Event{
				Type:     "node_completed",
				Node:     node,
				Payload:  json.RawMessage(`{}`),
				CostMpts: cost,
			})
			return w.lastErr
		})

	sc.Step(`^node "([^"]*)" fails with a settled cost of (\d+) milli-points$`,
		func(ctx context.Context, node string, cost int64) error {
			w := world(ctx).Machine
			if len(w.commands) != 1 || w.commands[0].Type != "run_node" {
				return fmt.Errorf("commands %+v, want one run_node", w.commands)
			}
			if w.commands[0].Node != node {
				return fmt.Errorf("next node %q, want %q", w.commands[0].Node, node)
			}
			w.snapshot, w.commands, w.lastErr = w.machine.Apply(w.snapshot, flow.Event{
				Type:     "node_failed",
				Node:     node,
				Payload:  json.RawMessage(`{"code":"failed"}`),
				CostMpts: cost,
			})
			return w.lastErr
		})

	sc.Step(`^the run's settled cost is (\d+) milli-points$`, func(ctx context.Context, want int64) error {
		w := world(ctx).Machine
		if w.snapshot.Cost != want {
			return fmt.Errorf("run cost %d milli-points, want %d", w.snapshot.Cost, want)
		}
		return nil
	})
}

func (w *machineWorld) declareGuardedLoop(name, from, fromKind, repair, repairKind, back string, guard *flow.Guard) error {
	w.ir = &flow.IR{
		Name:    name,
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			from:   {Kind: fromKind},
			repair: {Kind: repairKind, Guard: guard},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: from},
			{From: from, To: repair},
			{From: repair, To: back},
		},
	}
	return nil
}
