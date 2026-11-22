package temporal

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestCheckpointThresholdBoundary(t *testing.T) {
	cases := []struct {
		name    string
		hist    int
		th      int
		execs   int
		contEnd bool
	}{
		{"equal", 20, 20, 2, true},
		{"below", 19, 20, 1, false},
		{"zero", 0, 20, 1, false},
		{"large_threshold", 5, 100, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log execRunLog[[]error]
			rt := chainRuntime(t, tc.th)
			mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
				rec := log.begin()
				if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
					return nil, err
				}
				*rec = append(*rec, dc.Checkpoint(runtime.SnapshotRef{}))
				return json.RawMessage(`"ok"`), nil
			})
			opts := defaultChainOpts()
			opts.histLen = tc.hist
			res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, opts)
			if res.Execs != tc.execs {
				t.Fatalf("execs=%d want %d", res.Execs, tc.execs)
			}
			rec := mustExecRunLogEntry(t, &log, 0)
			if tc.contEnd {
				if !isContinueAsNewWorkflowErr((*rec)[0]) {
					t.Fatalf("want continue err got %v", (*rec)[0])
				}
			} else if (*rec)[0] != nil {
				t.Fatalf("want nil ckpt got %v", (*rec)[0])
			}
		})
	}
}

func TestCheckpointDefaultThresholdIs2000(t *testing.T) {
	opts := testOptions(t)
	opts.CheckpointEventThreshold = 0
	rt := mustNew(t, opts)
	mustRegisterStep(t, rt, "s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		return json.RawMessage(`"ok"`), nil
	})
	for _, tc := range []struct {
		hl    int
		execs int
	}{
		{1999, 1},
		{2000, 2},
	} {
		copts := defaultChainOpts()
		copts.histLen = tc.hl
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, copts)
		if res.Execs != tc.execs {
			t.Fatalf("hist %d execs=%d want %d", tc.hl, res.Execs, tc.execs)
		}
	}
}

func TestCheckpointNegativeThresholdNeverContinues(t *testing.T) {
	var ckErr error
	rt := chainRuntime(t, -1)
	opts := defaultChainOpts()
	opts.histLen = 1_000_000
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		ckErr = dc.Checkpoint(runtime.SnapshotRef{})
		return json.RawMessage(`"ok"`), nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, opts)
	if res.Execs != 1 || ckErr != nil || string(res.Output) != `"ok"` {
		t.Fatalf("execs=%d ckErr=%v out=%s", res.Execs, ckErr, res.Output)
	}
}

func TestCheckpointBelowThresholdDoesNotWaitForInFlightStep(t *testing.T) {
	start := chainFixedStart
	regSlow := func(t *testing.T, rt *Runtime) {
		t.Helper()
		mustRegisterStep(t, rt, "slow", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"ok"`), nil
		}, runtime.StepDefaults{})
	}
	t.Run("below", func(t *testing.T) {
		opts := testOptions(t)
		opts.CheckpointEventThreshold = 100
		rt := mustNew(t, opts)
		regSlow(t, rt)
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			dc.Step("slow", nil, runtime.StepOptions{})
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			return json.RawMessage(`"below"`), nil
		})
		var actDone int32
		var endClock time.Time
		belowOpts := defaultChainOpts()
		belowOpts.histLen = 10
		belowOpts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
			func(_ int, env *testsuite.TestWorkflowEnvironment) {
				env.SetOnActivityCompletedListener(func(_ *activity.Info, _ converter.EncodedValue, _ error) {
					atomic.StoreInt32(&actDone, 1)
				})
				env.OnActivity(stepActivityName, mock.Anything, mock.MatchedBy(func(in StepActivityInput) bool {
					return in.Name == "slow"
				})).After(time.Hour).Return(json.RawMessage(`"ok"`), nil)
			},
		}
		belowOpts.afterExec = func(_ int, env *testsuite.TestWorkflowEnvironment) {
			endClock = env.Now()
		}
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, belowOpts)
		if res.Execs != 1 || string(res.Output) != `"below"` {
			t.Fatalf("execs=%d out=%s", res.Execs, res.Output)
		}
		if actDone != 0 {
			t.Fatal("activity completed before workflow end")
		}
		if endClock.Sub(start) >= time.Hour {
			t.Fatal("clock advanced full hour on below-threshold path")
		}
	})
	t.Run("at_threshold", func(t *testing.T) {
		opts := testOptions(t)
		opts.CheckpointEventThreshold = 100
		rt := mustNew(t, opts)
		regSlow(t, rt)
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			dc.Step("slow", nil, runtime.StepOptions{})
			return nil, dc.Checkpoint(runtime.SnapshotRef{})
		})
		var controlClock time.Time
		atOpts := defaultChainOpts()
		atOpts.histLen = 100
		atOpts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
			func(_ int, env *testsuite.TestWorkflowEnvironment) {
				env.OnActivity(stepActivityName, mock.Anything, mock.MatchedBy(func(in StepActivityInput) bool {
					return in.Name == "slow"
				})).After(time.Hour).Return(json.RawMessage(`"ok"`), nil)
			},
		}
		atOpts.afterExec = func(exec int, env *testsuite.TestWorkflowEnvironment) {
			if exec == 0 {
				controlClock = env.Now()
			}
		}
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r2"}, atOpts)
		if res.Execs != 2 {
			t.Fatalf("control execs=%d", res.Execs)
		}
		if controlClock.Sub(start) < time.Hour {
			t.Fatal("control: clock did not advance hour")
		}
	})
}

func TestCheckpointAtThresholdWaitsForInFlightStepAndDoesNotRunItAgain(t *testing.T) {
	opts := testOptions(t)
	opts.CheckpointEventThreshold = 15
	rt := mustNew(t, opts)
	mustRegisterStep(t, rt, "slow", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"step-ok"`), nil
	}, runtime.StepDefaults{})
	var actDone, actStarted atomic.Int32
	var log execRunLog[struct {
		actDone int32
		stepRes json.RawMessage
	}]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		sf := dc.Step("slow", nil, runtime.StepOptions{})
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			rec.actDone = actDone.Load()
			return nil, err
		}
		if gerr := sf.Get(&rec.stepRes); gerr != nil {
			return nil, gerr
		}
		return rec.stepRes, nil
	})
	chainOpts := defaultChainOpts()
	chainOpts.histLen = 20
	chainOpts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.SetOnActivityStartedListener(func(_ *activity.Info, _ context.Context, _ converter.EncodedValues) {
				actStarted.Add(1)
			})
			env.SetOnActivityCompletedListener(func(_ *activity.Info, _ converter.EncodedValue, _ error) {
				actDone.Store(1)
			})
			env.OnActivity(stepActivityName, mock.Anything, mock.MatchedBy(func(in StepActivityInput) bool {
				return in.Name == "slow"
			})).After(time.Minute).Return(json.RawMessage(`"step-ok"`), nil)
		},
	}
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"}, chainOpts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	if actStarted.Load() != 1 {
		t.Fatalf("activity started %d", actStarted.Load())
	}
	e1 := mustExecRunLogEntry(t, &log, 0)
	if e1.actDone != 1 {
		t.Fatalf("at ckpt act done=%d", e1.actDone)
	}
	e2 := mustExecRunLogEntry(t, &log, 1)
	if string(e2.stepRes) != `"step-ok"` {
		t.Fatalf("exec2 step %s", e2.stepRes)
	}
	if string(res.Output) != `"step-ok"` {
		t.Fatalf("output %s", res.Output)
	}
}

func TestCheckpointAtThresholdWaitsForInFlightChildAndDoesNotRunItAgain(t *testing.T) {
	opts := testOptions(t)
	opts.CheckpointEventThreshold = 15
	rt := mustNew(t, opts)
	var childRuns atomic.Int32
	var childDone atomic.Int32
	var log execRunLog[struct {
		childDone int32
		childRes  json.RawMessage
	}]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, ref runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		switch ref.Name {
		case "parent":
			rec := log.begin()
			cf := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ID: "ch1"})
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				rec.childDone = childDone.Load()
				return nil, err
			}
			if gerr := cf.Get(&rec.childRes); gerr != nil {
				return nil, gerr
			}
			return rec.childRes, nil
		case "child-flow":
			childRuns.Add(1)
			if err := dc.Sleep(2 * time.Minute).Get(nil); err != nil {
				return nil, err
			}
			return json.RawMessage(`"child-done"`), nil
		default:
			return nil, core.NewError(core.CodeNotFound, "flow", nil)
		}
	})
	chainOpts := defaultChainOpts()
	chainOpts.histLen = 20
	chainOpts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.SetOnChildWorkflowCompletedListener(func(_ *workflow.Info, _ converter.EncodedValue, _ error) {
				childDone.Store(1)
			})
		},
	}
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"}, chainOpts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	if childRuns.Load() != 1 {
		t.Fatalf("child runs %d", childRuns.Load())
	}
	e1 := mustExecRunLogEntry(t, &log, 0)
	if e1.childDone != 1 {
		t.Fatalf("at ckpt child done=%d", e1.childDone)
	}
	e2 := mustExecRunLogEntry(t, &log, 1)
	if string(e2.childRes) != `"child-done"` {
		t.Fatalf("exec2 child %s", e2.childRes)
	}
	if string(res.Output) != `"child-done"` {
		t.Fatalf("output %s", res.Output)
	}
}
