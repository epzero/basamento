package host_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

func TestIdempotencyKeysDifferAcrossVisitsAndMatchOnReplay(t *testing.T) {
	def := newFlow("loop")
	def.Nodes["work"] = flow.Node{
		Kind:  "tool",
		Spec:  json.RawMessage(`{"tool":"w1"}`),
		Guard: &flow.Guard{MaxVisits: 2, While: `true`},
	}
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "work"},
		{From: "work", To: "work"},
	}
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`{"ok":true}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("first run: %v", err)
	}
	journal := dc.copyJournal()
	var keys []string
	for _, e := range journal {
		if e.kind == "step" {
			keys = append(keys, e.stepOpts.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("visit keys %+v, want two distinct keys", keys)
	}
	want0 := fmt.Sprintf("loop@%s/work#0@1", ref.Hash)
	want1 := fmt.Sprintf("loop@%s/work#0@2", ref.Hash)
	if keys[0] != want0 || keys[1] != want1 {
		t.Fatalf("keys %+v, want %q and %q", keys, want0, want1)
	}
	replay := replayFakeDC(journal)
	if _, err := h.Run(replay, ref, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("replay: %v", err)
	}
	var replayKeys []string
	for _, c := range replay.callLog() {
		if c.kind == "step" {
			replayKeys = append(replayKeys, c.stepOpts.IdempotencyKey)
		}
	}
	if fmt.Sprint(replayKeys) != fmt.Sprint(keys) {
		t.Fatalf("replay keys %v, want %v", replayKeys, keys)
	}
}

func TestMapFanOutUsesDistinctIdempotencyKeys(t *testing.T) {
	def := newFlow("mapped")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`)
	def.Nodes["fan"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Edges = []flow.Edge{{From: ir.Start, To: "fan"}, {From: "fan", To: "work"}}
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`"x"`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(dc, ref, json.RawMessage(`{"items":[1,2,3]}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range dc.callLog() {
		if c.kind != "step" {
			continue
		}
		k := c.stepOpts.IdempotencyKey
		if seen[k] {
			t.Fatalf("duplicate idempotency key %q", k)
		}
		seen[k] = true
	}
	if len(seen) != 3 {
		t.Fatalf("got %d keys, want 3", len(seen))
	}
	wantKeys := []string{
		fmt.Sprintf("mapped@%s/work#1@1", ref.Hash),
		fmt.Sprintf("mapped@%s/work#2@1", ref.Hash),
		fmt.Sprintf("mapped@%s/work#3@1", ref.Hash),
	}
	got := make([]string, 0, 3)
	for _, c := range dc.callLog() {
		if c.kind == "step" {
			got = append(got, c.stepOpts.IdempotencyKey)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(wantKeys) {
		t.Fatalf("keys %v, want %v", got, wantKeys)
	}
}

func TestIdempotencyKeyUsesDefinitionNameNotCallerRefName(t *testing.T) {
	def := newFlow("real")
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	addLinear(def, "work")
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`{}`)}
	cat := newCatalog()
	ref := cat.register(def)
	alias := runtime.FlowRef{Name: "typo", Hash: ref.Hash}
	h := host.New(cat)
	if _, err := h.Run(dc, alias, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	wantPrefix := "real@" + ref.Hash + "/"
	for _, c := range dc.callLog() {
		if c.kind != "step" {
			continue
		}
		k := c.stepOpts.IdempotencyKey
		if !strings.HasPrefix(k, wantPrefix) {
			t.Fatalf("key %q, want prefix %q", k, wantPrefix)
		}
		if strings.HasPrefix(k, "typo@") {
			t.Fatalf("key %q uses caller ref name", k)
		}
	}
}

func TestIdempotencyKeyFormatPinned(t *testing.T) {
	def := newFlow("fmt")
	back := node("tool", `{"tool":"w1"}`, "", "")
	back.Guard = &flow.Guard{MaxVisits: 2, While: `true`}
	def.Nodes["first"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Nodes["back"] = back
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "first"},
		{From: "first", To: "back"},
		{From: "back", To: "back"},
	}
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`{}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("run: %v", err)
	}
	var keys []string
	for _, c := range dc.callLog() {
		if c.kind == "step" {
			keys = append(keys, c.stepOpts.IdempotencyKey)
		}
	}
	if len(keys) != 3 {
		t.Fatalf("step keys %+v, want 3", keys)
	}
	want := []string{
		fmt.Sprintf("fmt@%s/first#0@1", ref.Hash),
		fmt.Sprintf("fmt@%s/back#0@2", ref.Hash),
		fmt.Sprintf("fmt@%s/back#0@3", ref.Hash),
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("key[%d] %q, want %q", i, keys[i], want[i])
		}
	}
}
