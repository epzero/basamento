package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

const maxHandlerGuardRuns = 100

func checkHandlerRunLimit(node string, counts map[string]int) error {
	counts[node]++
	if counts[node] > maxHandlerGuardRuns {
		return core.NewError(core.CodeInternal,
			fmt.Sprintf("flow host test: node %q ran more than %d times: a guard did not stop the loop", node, maxHandlerGuardRuns),
			nil)
	}
	return nil
}
func decodeArgument(in any) json.RawMessage {
	switch value := in.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	default:
		raw, _ := json.Marshal(value)
		return raw
	}
}

// --- a definition catalog --------------------------------------------------

type catalog struct {
	byHash map[string]*flow.IR
	latest map[string]*flow.IR
}

func newCatalog() *catalog {
	return &catalog{byHash: map[string]*flow.IR{}, latest: map[string]*flow.IR{}}
}

func (c *catalog) register(def *flow.IR) runtime.FlowRef {
	hash, err := ir.Hash(def)
	if err != nil {
		panic(err)
	}
	c.byHash[hash] = def
	c.latest[def.Name] = def
	return runtime.FlowRef{Name: def.Name, Hash: hash}
}

func (c *catalog) Load(_ context.Context, ref runtime.FlowRef) (*flow.IR, error) {
	if ref.Hash != "" {
		def, ok := c.byHash[ref.Hash]
		if !ok {
			return nil, core.NewError(core.CodeNotFound, "definition not found", nil)
		}
		return def, nil
	}
	def, ok := c.latest[ref.Name]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "definition not found", nil)
	}
	return def, nil
}

// --- flow builders ---------------------------------------------------------

func newFlow(name string) *flow.IR {
	return &flow.IR{
		Name:    name,
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"v":{"type":"integer"},"worker":{"type":"object"},"out":{"type":"object"},"log":{"type":"array"}}}`),
		},
		Nodes: map[string]flow.Node{},
		Edges: []flow.Edge{},
	}
}

func addLinear(def *flow.IR, nodes ...string) {
	for i, id := range nodes {
		if i == 0 {
			def.Edges = append(def.Edges, flow.Edge{From: ir.Start, To: id})
			continue
		}
		def.Edges = append(def.Edges, flow.Edge{From: nodes[i-1], To: id})
	}
}

func node(kind, spec string, in, out string) flow.Node {
	n := flow.Node{Kind: kind, In: in, Out: out}
	if spec != "" {
		n.Spec = json.RawMessage(spec)
	}
	return n
}
func callFingerprints(calls []call) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		switch c.kind {
		case "step":
			out = append(out, fmt.Sprintf("step:%s:%s", c.name, c.stepOpts.IdempotencyKey))
		case "child":
			out = append(out, fmt.Sprintf("child:%s:%s", c.child.Name, c.childOpts.ID))
		case "gate":
			out = append(out, fmt.Sprintf("gate:%s", c.gate.Name))
		case "timer":
			out = append(out, fmt.Sprintf("timer:%s", c.duration))
		case "id":
			out = append(out, "id:"+c.childOpts.ID)
		default:
			out = append(out, c.kind)
		}
	}
	return out
}

func slicesContainsKind(fps []string, kind string) bool {
	prefix := kind + ":"
	for _, fp := range fps {
		if fp == kind || strings.HasPrefix(fp, prefix) {
			return true
		}
	}
	return false
}

func kinds(calls []call) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		switch c.kind {
		case "step":
			out[i] = "step:" + c.name
		case "timer":
			out[i] = "timer:" + c.duration.String()
		case "gate":
			out[i] = "gate:" + c.gate.Name
		case "child":
			out[i] = "child:" + c.child.Name
		}
	}
	return out
}

func waitFor(t *testing.T, dc *fakeDC, condition func(*fakeDC) bool) {
	t.Helper()
	dc.mu.Lock()
	for {
		if dc.finished {
			dc.mu.Unlock()
			t.Fatal("run finished before condition")
			return
		}
		if condition(dc) {
			dc.mu.Unlock()
			return
		}
		dc.cond.Wait()
	}
}

func assertJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	if !jsonEqual(got, json.RawMessage(want)) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func jsonEqual(left, right json.RawMessage) bool {
	var a, b any
	decoder := json.NewDecoder(bytes.NewReader(left))
	decoder.UseNumber()
	if err := decoder.Decode(&a); err != nil {
		return false
	}
	decoder = json.NewDecoder(bytes.NewReader(right))
	decoder.UseNumber()
	if err := decoder.Decode(&b); err != nil {
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
