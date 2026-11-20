package host_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

func TestReplayMakesTheSameCallsWithoutExecutingHandlerStepsAgain(t *testing.T) {
	def := newFlow("replay")
	def.Nodes["a"] = node("tool", `{"tool":"a"}`, "", "")
	def.Nodes["b"] = node("wait", `{"duration":"10s"}`, "", "")
	def.Nodes["c"] = node("agent", `{"agent":"c"}`, "", "")
	addLinear(def, "a", "b", "c")

	first := newFakeDC()
	first.instant = true
	first.steps["a"] = scripted{failTimes: 4, result: json.RawMessage(`{"t":1}`)}
	first.steps["c_step"] = scripted{result: json.RawMessage(`{"ok":true}`)}

	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(dc runtime.DurableContext, _ flow.Node, input json.RawMessage) (host.NodeResult, error) {
		var out json.RawMessage
		if err := dc.Step("c_step", input, runtime.StepOptions{}).Get(&out); err != nil {
			return host.NodeResult{}, err
		}
		return host.NodeResult{Output: out}, nil
	})

	if _, err := h.Run(first, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	journal := first.copyJournal()
	replay := replayFakeDC(journal)
	if _, err := h.Run(replay, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.diverged != nil {
		t.Fatalf("replay diverged: %v", replay.diverged)
	}
	if got, want := kinds(replay.callLog()), kinds(first.callLog()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("replay calls %v, want %v", got, want)
	}
	if count := replay.stepExecutions()["c_step"]; count != 0 {
		t.Fatalf("c_step ran %d times in replay, want it taken from history", count)
	}
	if first.stepRun["a"] != 5 {
		t.Fatalf("tool a ran %d times in first run, want 5 (4 fails + success)", first.stepRun["a"])
	}
}

func TestReplayCoversChildGateAndMap(t *testing.T) {
	def := newFlow("replay-mix")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"},"childOut":{"type":"object"}}}`)
	def.Nodes["sub"] = node("subflow", `{"flow":"child"}`, "", "childOut")
	def.Nodes["hold"] = node("gate", `{"schema":"r.json","accept_if":"true","timeout":"1h"}`, "", "")
	def.Nodes["pause"] = node("wait", `{"duration":"1s"}`, "", "")
	def.Nodes["fan"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "sub"},
		{From: "sub", To: "hold"},
		{From: "hold", To: "pause"},
		{From: "pause", To: "fan"},
		{From: "fan", To: "work"},
	}
	dc := newFakeDC()
	dc.steps["child:child"] = scripted{result: json.RawMessage(`{"child":1}`)}
	dc.steps["w1"] = scripted{result: json.RawMessage(`"x"`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	rh := dc.startRun(h, ref, json.RawMessage(`{"items":["a","b"]}`))
	waitFor(t, dc, func(d *fakeDC) bool {
		for _, c := range d.calls {
			if c.kind == "gate" {
				return true
			}
		}
		return false
	})
	dc.resolveGate("hold", json.RawMessage(`{"ok":true}`))
	dc.settle()
	dc.advance(time.Second)
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("first run: %v", rh.err)
	}
	wantFP := callFingerprints(dc.callLog())
	for _, kind := range []string{"child", "gate", "timer", "step"} {
		if !slicesContainsKind(wantFP, kind) {
			t.Fatalf("first run missing %s, fingerprints %v", kind, wantFP)
		}
	}
	journal := dc.copyJournal()
	idCount := 0
	for _, e := range journal {
		if e.kind == "id" {
			idCount++
		}
	}
	if idCount < 1 {
		t.Fatalf("journal has %d id entries, want at least one for subflow NewID", idCount)
	}
	t.Run("replay matches journal", func(t *testing.T) {
		replay := replayFakeDC(journal)
		if _, err := h.Run(replay, ref, json.RawMessage(`{"items":["a","b"]}`)); err != nil {
			t.Fatalf("replay: %v", err)
		}
		if replay.diverged != nil {
			t.Fatalf("diverged: %v", replay.diverged)
		}
		got := callFingerprints(replay.callLog())
		if fmt.Sprint(got) != fmt.Sprint(wantFP) {
			t.Fatalf("replay fingerprints %v, want %v", got, wantFP)
		}
	})
	t.Run("gate spec mismatch diverges", func(t *testing.T) {
		bad := append([]journalEntry(nil), journal...)
		for i := range bad {
			if bad[i].kind == "gate" {
				bad[i].gate.Timeout += time.Second
				break
			}
		}
		replay := replayFakeDC(bad)
		_, err := h.Run(replay, ref, json.RawMessage(`{"items":["a","b"]}`))
		if core.CodeOf(err) != core.CodeDeterminismBroken && replay.diverged == nil {
			t.Fatalf("want determinism broken, got %v diverged=%v", err, replay.diverged)
		}
	})
	t.Run("child id mismatch diverges", func(t *testing.T) {
		bad := append([]journalEntry(nil), journal...)
		for i := range bad {
			if bad[i].kind == "child" {
				bad[i].childOpts.ID = "changed"
				break
			}
		}
		replay := replayFakeDC(bad)
		_, err := h.Run(replay, ref, json.RawMessage(`{"items":["a","b"]}`))
		if core.CodeOf(err) != core.CodeDeterminismBroken && replay.diverged == nil {
			t.Fatalf("want determinism broken, got %v diverged=%v", err, replay.diverged)
		}
	})
}

func TestReplayDetectsStepNameMismatch(t *testing.T) {
	def := newFlow("replay-div")
	def.Nodes["a"] = node("tool", `{"tool":"a"}`, "", "")
	addLinear(def, "a")
	first := newFakeDC()
	first.steps["a"] = scripted{result: json.RawMessage(`{}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(first, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first: %v", err)
	}
	journal := first.copyJournal()
	journal[0].name = "b"
	replay := replayFakeDC(journal)
	_, err := h.Run(replay, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeDeterminismBroken && replay.diverged == nil {
		t.Fatalf("want determinism broken, got %v diverged=%v", err, replay.diverged)
	}
}
