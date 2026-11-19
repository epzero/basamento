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
// commands, and the snapshots already recorded for other completion orders.
type machineWorld struct {
	ir        *flow.IR
	machine   *machine.Machine
	snapshot  flow.Snapshot
	commands  []flow.Command
	lastErr   error
	snapshots map[string]flow.Snapshot
}

func newMachineWorld() *machineWorld {
	return &machineWorld{snapshots: map[string]flow.Snapshot{}}
}

func (w *machineWorld) start(input string) error {
	m, err := machine.New(w.ir)
	if err != nil {
		return err
	}
	w.machine = m
	w.snapshot, w.commands, w.lastErr = m.Start(json.RawMessage(input))
	return w.lastErr
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
			w.snapshots[strings.Join([]string{first, second, third}, ",")] = w.snapshot
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
}
