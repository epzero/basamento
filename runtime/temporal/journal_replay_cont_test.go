package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestContinuationCarriesEntriesAcrossTwoContinuations(t *testing.T) {
	rt := chainRuntime(t, 8)
	type carryValueRecord struct {
		Nows    []time.Time `json:"nows"`
		Choices []int       `json:"choices"`
	}
	var log execRunLog[carryValueRecord]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		rec.Nows = append(rec.Nows, dc.Now())
		if err := dc.Sleep(time.Minute).Get(nil); err != nil {
			return nil, err
		}
		c, err := dc.Select(dc.Sleep(time.Hour), dc.Sleep(time.Minute))
		if err != nil {
			return nil, err
		}
		rec.Choices = append(rec.Choices, c)
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		rec.Nows = append(rec.Nows, dc.Now())
		if err := dc.Sleep(time.Minute).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		rec.Nows = append(rec.Nows, dc.Now())
		return json.Marshal(rec)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 3 {
		t.Fatalf("execs=%d", res.Execs)
	}
	c1 := res.Inputs[1].Continuation
	if c1 == nil {
		t.Fatal("no cont1")
	}
	if len(c1.Selects) != 1 || len(c1.Nows) != 1 || c1.Checkpoints != 1 {
		t.Fatalf("cont1 selects=%d nows=%d ckpt=%d", len(c1.Selects), len(c1.Nows), c1.Checkpoints)
	}
	var fired int
	for _, tm := range c1.Timers {
		if tm.Fired {
			fired++
		}
	}
	if fired < 1 {
		t.Fatalf("cont1 fired timers=%d want at least 1", fired)
	}
	c2 := res.Inputs[2].Continuation
	if c2 == nil {
		t.Fatal("cont2 missing")
	}
	if c2.Checkpoints != 2 {
		t.Fatalf("cont2 ckpt=%d", c2.Checkpoints)
	}
	if len(c2.Selects) < len(c1.Selects) || c2.Selects[0] != c1.Selects[0] {
		t.Fatalf("cont2 selects prefix %+v vs %+v", c2.Selects, c1.Selects)
	}
	if len(c2.Nows) < len(c1.Nows) {
		t.Fatalf("cont2 nows=%d shorter than cont1 %d", len(c2.Nows), len(c1.Nows))
	}
	for i := range c1.Nows {
		if !c2.Nows[i].Equal(c1.Nows[i]) {
			t.Fatalf("nows[%d] diverged", i)
		}
	}
	if len(c2.Timers) < len(c1.Timers) {
		t.Fatalf("cont2 timers shorter than cont1")
	}
	for i := range c1.Timers {
		if !c2.Timers[i].Deadline.Equal(c1.Timers[i].Deadline) || c2.Timers[i].Fired != c1.Timers[i].Fired {
			t.Fatalf("timers[%d] diverged", i)
		}
	}
	var out carryValueRecord
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Nows) != 3 || len(out.Choices) != 1 {
		t.Fatalf("output nows=%d choices=%d", len(out.Nows), len(out.Choices))
	}
}

func TestContinuationEmittedEventsStayOnceInOrderWithDenseSeq(t *testing.T) {
	ob := newRecordingOutbox()
	opts := testOptions(t)
	opts.Outbox = ob
	opts.CheckpointEventThreshold = 10
	rt := mustNew(t, opts)
	mustRegisterStep(t, rt, "s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Emit(runtime.ProgressEvent{Type: "a"}); err != nil {
			return nil, err
		}
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		if err := dc.Emit(runtime.ProgressEvent{Type: "b"}); err != nil {
			return nil, err
		}
		return json.RawMessage(`"ok"`), nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	if len(ob.calls) != 2 || ob.calls[0].seq != 1 || ob.calls[0].typ != "a" ||
		ob.calls[1].seq != 2 || ob.calls[1].typ != "b" {
		t.Fatalf("calls %+v", ob.calls)
	}
	if ob.calls[0].runID != "r1" || ob.calls[0].tenant != "acme" {
		t.Fatalf("tenant/run %+v", ob.calls[0])
	}

	t.Run("two_checkpoints", func(t *testing.T) {
		ob2 := newRecordingOutbox()
		opts2 := testOptions(t)
		opts2.Outbox = ob2
		opts2.CheckpointEventThreshold = 8
		rt2 := mustNew(t, opts2)
		mustRegisterStep(t, rt2, "s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"ok"`), nil
		}, runtime.StepDefaults{})
		mustRegisterFlow(t, rt2, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			if err := dc.Emit(runtime.ProgressEvent{Type: "a"}); err != nil {
				return nil, err
			}
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := dc.Emit(runtime.ProgressEvent{Type: "b"}); err != nil {
				return nil, err
			}
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := dc.Emit(runtime.ProgressEvent{Type: "c"}); err != nil {
				return nil, err
			}
			return json.RawMessage(`"ok"`), nil
		})
		res2 := runChain(t, rt2, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r2"}, defaultChainOpts())
		if res2.Execs != 3 {
			t.Fatalf("execs=%d", res2.Execs)
		}
		if len(ob2.calls) != 3 {
			t.Fatalf("calls %d", len(ob2.calls))
		}
		for i, want := range []string{"a", "b", "c"} {
			if ob2.calls[i].seq != uint64(i+1) || ob2.calls[i].typ != want {
				t.Fatalf("[%d] %+v", i, ob2.calls[i])
			}
		}
	})
}

func TestCheckpointAfterEveryStepTerminates(t *testing.T) {
	rt := chainRuntime(t, 5)
	counters := map[string]*atomic.Int32{}
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("n%d", i)
		c := &atomic.Int32{}
		counters[name] = c
		n := name
		mustRegisterStep(t, rt, n, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			counters[n].Add(1)
			return json.RawMessage(`"ok"`), nil
		}, runtime.StepDefaults{})
	}
	var log execRunLog[[]error]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		for i := 0; i < 4; i++ {
			if err := dc.Step(fmt.Sprintf("n%d", i), nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			err := dc.Checkpoint(runtime.SnapshotRef{})
			*rec = append(*rec, err)
			if err != nil {
				return nil, err
			}
		}
		return json.RawMessage(`"done"`), nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 5 {
		t.Fatalf("Execs=%d want 5", res.Execs)
	}
	if string(res.Output) != `"done"` {
		t.Fatalf("output %s", res.Output)
	}
	for _, c := range counters {
		if c.Load() != 1 {
			t.Fatalf("step ran %d times", c.Load())
		}
	}
	// Executions 0..3 each ended with continue; execution 4 completed.
	for exec := 0; exec < 5; exec++ {
		rec := log.at(exec)
		if rec == nil {
			t.Fatalf("exec %d missing", exec)
		}
		wantLen := exec + 1
		if exec == 4 {
			wantLen = 4
		}
		if len(*rec) != wantLen {
			t.Fatalf("exec %d ckpt results %d want %d", exec, len(*rec), wantLen)
		}
		for k := 0; k < wantLen; k++ {
			if k < exec {
				if (*rec)[k] != nil {
					t.Fatalf("exec %d ckpt[%d]=%v want nil", exec, k, (*rec)[k])
				}
			} else if k == exec && exec < 4 {
				if !isContinueAsNewWorkflowErr((*rec)[k]) {
					t.Fatalf("exec %d ckpt[%d] not continue", exec, k)
				}
			} else if exec == 4 && (*rec)[k] != nil {
				t.Fatalf("exec 4 ckpt[%d]=%v want nil", k, (*rec)[k])
			}
		}
	}
}

func TestContinuationJSONShape(t *testing.T) {
	td := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	contGolden := `{"steps":{"s":{"result":"ok"}},"children":{"child-1":{"flow":{"Name":"child-flow","Hash":""},"result":"child-ok"}},"gates":{"g1":{"name":"g1","timeout_ns":1000000000,"deadline":"2024-06-15T12:05:00Z","resolved":true,"payload":"p"},"g2":{"name":"g2","timeout_ns":2000000000,"deadline":"2024-06-15T12:10:00Z","ended_by_timeout":true},"g3":{"name":"g3","deadline":"2024-06-15T12:00:00Z","ended_by_cancel":true}},"timers":[{"deadline":"2024-06-15T12:01:00Z","fired":true},{"deadline":"2024-06-15T13:00:00Z","fired":false}],"selects":[1,0],"nows":["2024-06-15T12:00:00Z","2024-06-15T12:30:00Z"],"emitted":2,"checkpoints":1}`
	hostWithGolden := `{"flow":{"Name":"flow-a","Hash":""},"input":"in","run_id":"run-1","continuation":` + contGolden + `}`
	hostNoContGolden := `{"flow":{"Name":"flow-b","Hash":""},"input":null,"run_id":"run-2"}`

	populated := Continuation{
		Steps: map[string]StepOutcome{
			"s": {Result: json.RawMessage(`"ok"`)},
		},
		Children: map[string]ChildOutcome{
			"child-1": {Flow: runtime.FlowRef{Name: "child-flow"}, Result: json.RawMessage(`"child-ok"`)},
		},
		Gates: map[string]GateJournal{
			"g1": {
				Name: "g1", TimeoutNs: int64(time.Second),
				Deadline: time.Date(2024, 6, 15, 12, 5, 0, 0, time.UTC),
				Resolved: true, Payload: json.RawMessage(`"p"`),
			},
			"g2": {
				Name: "g2", TimeoutNs: int64(2 * time.Second),
				Deadline:       time.Date(2024, 6, 15, 12, 10, 0, 0, time.UTC),
				EndedByTimeout: true,
			},
			"g3": {
				Name: "g3", Deadline: td, EndedByCancel: true,
			},
		},
		Timers: []TimerJournal{
			{Deadline: time.Date(2024, 6, 15, 12, 1, 0, 0, time.UTC), Fired: true},
			{Deadline: time.Date(2024, 6, 15, 13, 0, 0, 0, time.UTC), Fired: false},
		},
		Selects:     []int{1, 0},
		Nows:        []time.Time{td, time.Date(2024, 6, 15, 12, 30, 0, 0, time.UTC)},
		Emitted:     2,
		Checkpoints: 1,
	}
	withCont := HostInput{
		Flow:         runtime.FlowRef{Name: "flow-a"},
		Input:        json.RawMessage(`"in"`),
		RunID:        "run-1",
		Continuation: &populated,
	}
	withoutCont := HostInput{
		Flow:  runtime.FlowRef{Name: "flow-b"},
		RunID: "run-2",
	}

	for _, tc := range []struct {
		name   string
		in     HostInput
		golden string
	}{
		{"with_continuation", withCont, hostWithGolden},
		{"without_continuation", withoutCont, hostNoContGolden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.golden {
				t.Fatalf("marshal mismatch\n got: %s\nwant: %s", got, tc.golden)
			}
			var decoded HostInput
			if err := json.Unmarshal([]byte(tc.golden), &decoded); err != nil {
				t.Fatal(err)
			}
			if tc.in.Input == nil {
				decoded.Input = nil
			}
			assertDeepEqual(t, tc.in, decoded)
		})
	}
	contOnly, err := json.Marshal(populated)
	if err != nil {
		t.Fatal(err)
	}
	var decodedCont Continuation
	if err := json.Unmarshal(contOnly, &decodedCont); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedCont, populated) {
		t.Fatalf("cont roundtrip %+v", decodedCont)
	}
}
