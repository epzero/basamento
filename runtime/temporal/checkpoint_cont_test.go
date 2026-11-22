package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestCheckpointReplayedOrdinalReturnsNilWithoutChecking(t *testing.T) {
	var log execRunLog[[]error]
	rt := chainRuntime(t, 10)
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		*rec = append(*rec, dc.Checkpoint(runtime.SnapshotRef{}))
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		*rec = append(*rec, dc.Checkpoint(runtime.SnapshotRef{}))
		return json.RawMessage(`"ok"`), nil
	})
	opts := defaultChainOpts()
	opts.histLen = 25
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, opts)
	if res.Execs != 3 {
		t.Fatalf("execs=%d", res.Execs)
	}
	want := [][]bool{{false}, {true, false}, {true, true}}
	for exec, pattern := range want {
		rec := mustExecRunLogEntry(t, &log, exec)
		for i, replayed := range pattern {
			if replayed {
				if (*rec)[i] != nil {
					t.Fatalf("exec %d ckpt %d=%v want nil", exec, i, (*rec)[i])
				}
			} else if !isContinueAsNewWorkflowErr((*rec)[i]) {
				t.Fatalf("exec %d ckpt %d want continue", exec, i)
			}
		}
	}
}

func TestCheckpointErrorIgnoredByFlowStillContinuesAsNew(t *testing.T) {
	rt := chainRuntime(t, 10)
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		_ = dc.Checkpoint(runtime.SnapshotRef{}) // flow intentionally ignores checkpoint error
		return json.RawMessage(`"ok"`), nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 || string(res.Output) != `"ok"` {
		t.Fatalf("execs=%d out=%s", res.Execs, res.Output)
	}
	if !isContinueAsNewWorkflowErr(res.PerErr[0]) {
		t.Fatalf("exec0 err %v", res.PerErr[0])
	}
}

func TestDurableCallsAfterContinueDecisionFailFastButNewIDRandNowWork(t *testing.T) {
	ob := newRecordingOutbox()
	opts := testOptions(t)
	opts.Outbox = ob
	opts.CheckpointEventThreshold = 10
	rt := mustNew(t, opts)
	mustRegisterStep(t, rt, "s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"step-ok"`), nil
	}, runtime.StepDefaults{})
	mustRegisterStep(t, rt, "s2", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"s2"`), nil
	}, runtime.StepDefaults{})
	var childStarts atomic.Int32
	var log execRunLog[afterContinueSnapshot]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, ref runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if ref.Name == "child-flow" {
			return json.RawMessage(`"c"`), nil
		}
		rec := log.begin()
		f := dc.Sleep(time.Hour)
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			rec.recordAfterContinue(dc, f)
			return nil, err
		}
		rec.recordAfterReplaySuccess(dc, f)
		return json.RawMessage(`"ok"`), nil
	})
	chainOpts := defaultChainOpts()
	chainOpts.histLenFor = func(exec int) int {
		if exec >= 1 {
			return 5
		}
		return 25
	}
	chainOpts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.SetOnChildWorkflowStartedListener(func(_ *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
				childStarts.Add(1)
			})
		},
	}
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "p"}, RunID: "r1"}, chainOpts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	if childStarts.Load() != 0 {
		t.Fatalf("child starts %d", childStarts.Load())
	}
	if err := compareAfterContinueSnapshots(log.at(0), log.at(1)); err != nil {
		t.Fatal(err)
	}
}

type afterContinueSnapshot struct {
	step, sleep, await, child, emit, sel, ckpt2 *core.Error
	id                                          string
	rand                                        uint64
	nowZero                                     bool
	timerOK                                     bool
	stepOK                                      string
}

func (r *afterContinueSnapshot) recordAfterContinue(dc runtime.DurableContext, f runtime.Future) {
	r.id = dc.NewID("k")
	r.rand = dc.Rand("r").Uint64()
	now := dc.Now()
	r.nowZero = now.IsZero()
	r.step = coreErrorFrom(dc.Step("s2", nil, runtime.StepOptions{}).Get(nil))
	r.sleep = coreErrorFrom(dc.Sleep(time.Minute).Get(nil))
	r.await = coreErrorFrom(dc.Await(runtime.GateSpec{Name: "g"}).Get(nil))
	r.child = coreErrorFrom(dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ID: "x"}).Get(nil))
	r.emit = coreErrorFrom(dc.Emit(runtime.ProgressEvent{Type: "e"}))
	_, selErr := dc.Select(dc.Sleep(time.Minute))
	r.sel = coreErrorFrom(selErr)
	r.ckpt2 = coreErrorFrom(dc.Checkpoint(runtime.SnapshotRef{}))
	r.timerOK = f.Get(nil) == nil
}

func (r *afterContinueSnapshot) recordAfterReplaySuccess(dc runtime.DurableContext, f runtime.Future) {
	r.timerOK = f.Get(nil) == nil
	r.ckpt2 = coreErrorFrom(dc.Checkpoint(runtime.SnapshotRef{}))
	var out json.RawMessage
	if err := dc.Step("s2", nil, runtime.StepOptions{}).Get(&out); err != nil {
		r.step = coreErrorFrom(err)
	} else {
		r.stepOK = string(out)
	}
	if err := dc.Sleep(time.Minute).Get(nil); err != nil {
		r.sleep = coreErrorFrom(err)
	}
	if err := dc.Emit(runtime.ProgressEvent{Type: "e2"}); err != nil {
		r.emit = coreErrorFrom(err)
	}
}

func coreErrorFrom(err error) *core.Error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

func compareAfterContinueSnapshots(exec1, exec2 *afterContinueSnapshot) error {
	if exec1 == nil || exec2 == nil {
		return fmt.Errorf("missing log")
	}
	contMsg := "temporal: the run is continuing as new"
	for _, pe := range []*core.Error{exec1.step, exec1.sleep, exec1.await, exec1.child, exec1.emit, exec1.sel, exec1.ckpt2} {
		if pe == nil || pe.Code != core.CodeCancelled || pe.Message != contMsg {
			if pe == nil {
				return fmt.Errorf("exec1 fail-fast: missing cancelled-continue error")
			}
			return fmt.Errorf("exec1 fail-fast: code=%s msg=%q", pe.Code, pe.Message)
		}
	}
	if exec1.id != "k_1" || exec1.nowZero || !exec1.timerOK {
		return fmt.Errorf("exec1 id=%q nowZero=%v timer=%v", exec1.id, exec1.nowZero, exec1.timerOK)
	}
	if exec2.ckpt2 != nil {
		return fmt.Errorf("exec2 replayed ckpt code=%s msg=%q", exec2.ckpt2.Code, exec2.ckpt2.Message)
	}
	if !exec2.timerOK {
		return fmt.Errorf("exec2 timer")
	}
	if exec2.step != nil || exec2.sleep != nil || exec2.emit != nil {
		return fmt.Errorf("exec2 durable errs on replay (step/sleep/emit must be nil)")
	}
	if exec2.stepOK != `"s2"` {
		return fmt.Errorf("exec2 step %q", exec2.stepOK)
	}
	return nil
}

func TestUpdateValidatorAnswersUnavailableWhileContinuing(t *testing.T) {
	rt := chainRuntime(t, 5)
	var cbCount atomic.Int32
	var upd *gateUpdateRecord
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Sleep(time.Hour)
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			if gerr := f.Get(nil); gerr != nil {
				return nil, gerr
			}
			return nil, err
		}
		return json.RawMessage(`"ok"`), nil
	})
	opts := defaultChainOpts()
	opts.histLen = 10
	opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.RegisterDelayedCallback(func() {
				cbCount.Add(1)
				upd = sendGateResolveUpdate(env, "ja-upd", "g", json.RawMessage(`"p"`))
			}, time.Minute)
		},
	}
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d want 2", res.Execs)
	}
	if !isContinueAsNewWorkflowErr(res.PerErr[0]) {
		t.Fatalf("exec0 %v", res.PerErr[0])
	}
	if cbCount.Load() != 1 {
		t.Fatalf("callbacks %d", cbCount.Load())
	}
	if upd == nil {
		t.Fatal("missing gate update record")
	}
	if upd.accepted || upd.completed {
		t.Fatalf("accepted=%v completed=%v", upd.accepted, upd.completed)
	}
	var ae *sdktemporal.ApplicationError
	if !errors.As(upd.rejected, &ae) {
		t.Fatalf("reject %v", upd.rejected)
	}
	if ae.Type() != string(core.CodeUnavailable) || ae.Message() != "temporal: the run is continuing as new; retry" {
		t.Fatalf("reject type=%s msg=%s", ae.Type(), ae.Message())
	}
}

const cancelRunCheckpointMsg = "temporal: the run was cancelled"

func TestCheckpointOnCancelledRunReturnsCancelledAndDoesNotContinue(t *testing.T) {
	t.Run("at_threshold_decision", func(t *testing.T) {
		rt := chainRuntime(t, 10)
		var sleepCode core.Code
		var ckPE *core.Error
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			sleepErr := dc.Sleep(time.Hour).Get(nil)
			sleepCode = core.CodeOf(sleepErr)
			ckErr := dc.Checkpoint(runtime.SnapshotRef{})
			ckPE = coreErrorFrom(ckErr)
			return nil, ckErr
		})
		opts := defaultChainOpts()
		opts.allowFlowError = true
		opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
			func(exec int, env *testsuite.TestWorkflowEnvironment) {
				if exec != 0 {
					return
				}
				env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Minute)
			},
		}
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, opts)
		if res.Execs != 1 {
			t.Fatalf("execs=%d want 1", res.Execs)
		}
		if isContinueAsNewWorkflowErr(res.PerErr[0]) {
			t.Fatalf("exec0 must not continue-as-new: %v", res.PerErr[0])
		}
		if res.Err == nil {
			t.Fatal("workflow must not complete successfully")
		}
		if sleepCode != core.CodeCancelled {
			t.Fatalf("sleep code %s", sleepCode)
		}
		if ckPE == nil || ckPE.Code != core.CodeCancelled || ckPE.Message != cancelRunCheckpointMsg {
			t.Fatalf("checkpoint err %v", ckPE)
		}
	})
	t.Run("below_threshold_returns_nil", func(t *testing.T) {
		rt := chainRuntime(t, 10)
		var sleepCode core.Code
		var ckPE *core.Error
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			sleepErr := dc.Sleep(time.Hour).Get(nil)
			sleepCode = core.CodeOf(sleepErr)
			ckPE = coreErrorFrom(dc.Checkpoint(runtime.SnapshotRef{}))
			return nil, sleepErr
		})
		opts := defaultChainOpts()
		opts.histLen = 5
		opts.allowFlowError = true
		opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
			func(_ int, env *testsuite.TestWorkflowEnvironment) {
				env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Minute)
			},
		}
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, opts)
		if res.Execs != 1 {
			t.Fatalf("execs=%d", res.Execs)
		}
		if isContinueAsNewWorkflowErr(res.PerErr[0]) {
			t.Fatal("must not continue-as-new")
		}
		if sleepCode != core.CodeCancelled {
			t.Fatalf("sleep %s", sleepCode)
		}
		if ckPE != nil {
			t.Fatalf("below-threshold checkpoint %v want nil", ckPE)
		}
	})
	t.Run("replayed_ordinal_returns_nil", func(t *testing.T) {
		rt := chainRuntime(t, 10)
		var log execRunLog[*core.Error]
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			rec := log.begin()
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			*rec = coreErrorFrom(dc.Checkpoint(runtime.SnapshotRef{}))
			return json.RawMessage(`"ok"`), nil
		})
		opts := defaultChainOpts()
		opts.maxExec = 2
		opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
			nil,
			func(_ int, env *testsuite.TestWorkflowEnvironment) {
				env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, 0)
			},
		}
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"}, opts)
		if res.Execs != 2 || string(res.Output) != `"ok"` {
			t.Fatalf("execs=%d out=%s err=%v", res.Execs, res.Output, res.Err)
		}
		r1 := log.at(1)
		if r1 == nil {
			t.Fatal("missing exec1 log")
		}
		if *r1 != nil {
			t.Fatalf("replayed checkpoint on cancelled exec %v want nil", *r1)
		}
	})
}

func TestCheckpointCancelledDuringDrainReturnsCancelled(t *testing.T) {
	opts := testOptions(t)
	opts.CheckpointEventThreshold = 5
	rt := mustNew(t, opts)
	mustRegisterStep(t, rt, "slow", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	var ckCode core.Code
	var cbCount atomic.Int32
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		dc.Step("slow", nil, runtime.StepOptions{})
		err := dc.Checkpoint(runtime.SnapshotRef{})
		ckCode = core.CodeOf(err)
		return nil, err
	})
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Hour)
	env.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
	env.SetDataConverter(&clientConverter{inner: rt.DataConverter(), tenant: "acme"})
	env.SetCurrentHistoryLength(100)
	setTestTenantHeader(env, "acme")
	env.RegisterDelayedCallback(func() {
		cbCount.Add(1)
		env.CancelWorkflow()
	}, time.Second)
	mustRegister(t, rt, env)
	env.OnActivity(stepActivityName, mock.Anything, mock.Anything).After(time.Hour).Return(json.RawMessage(`"ok"`), nil)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	werr := env.GetWorkflowError()
	if workflow.IsContinueAsNewError(werr) {
		t.Fatal("cancel during drain must not continue-as-new")
	}
	if cbCount.Load() != 1 {
		t.Fatalf("callback count %d", cbCount.Load())
	}
	if ckCode != core.CodeCancelled {
		t.Fatalf("checkpoint code %s", ckCode)
	}
}
