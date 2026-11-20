package steps

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/flow"
)

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

// nodeStateResult is the run outcome for a node: the value at state[node.Out] in
// the run's final state. Nodes without Out cannot be read this way.
func (w *hostWorld) nodeStateResult(node string) (json.RawMessage, error) {
	if w.flow == nil {
		return nil, fmt.Errorf("flow host test: no flow declared")
	}
	n, ok := w.flow.Nodes[node]
	if !ok {
		return nil, fmt.Errorf("flow host test: node %q is not in the flow", node)
	}
	if n.Out == "" {
		return nil, fmt.Errorf("flow host test: node %q has no Out key for run-state result", node)
	}
	if w.result == nil {
		return nil, fmt.Errorf("flow host test: run has no final state")
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(w.result, &state); err != nil {
		return nil, fmt.Errorf("run final state %s: %w", w.result, err)
	}
	value, ok := state[n.Out]
	if !ok {
		return nil, fmt.Errorf("run final state has no key %q (Out of node %q)", n.Out, node)
	}
	return value, nil
}

func (w *hostWorld) nodeExpectsNoRunStateResult(node string) error {
	if w.flow == nil {
		return fmt.Errorf("flow host test: no flow declared")
	}
	n, ok := w.flow.Nodes[node]
	if !ok {
		return fmt.Errorf("flow host test: node %q is not in the flow", node)
	}
	if n.Out != "" {
		return fmt.Errorf("flow host test: node %q has Out %q, want a node with no output mapping", node, n.Out)
	}
	return nil
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
	if a.childOpts.ParentClose != b.childOpts.ParentClose {
		return fmt.Errorf("child ParentClose %q, want %q", a.childOpts.ParentClose, b.childOpts.ParentClose)
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
