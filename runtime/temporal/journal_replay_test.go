package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestContinuationDoesNotExecuteFinishedStepsAgain(t *testing.T) {
	counters := map[string]*atomic.Int32{}
	rt := chainRuntime(t, 10)
	registerCountingSteps(t, rt, []string{"x0", "x1", "x2", "x3", "x4", "x5"}, counters)
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		for i := 0; i < 3; i++ {
			if err := dc.Step(fmt.Sprintf("x%d", i), nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		for i := 3; i < 5; i++ {
			if err := dc.Step(fmt.Sprintf("x%d", i), nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		if err := dc.Step("x5", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 3 {
		t.Fatalf("Execs=%d want 3", res.Execs)
	}
	if string(res.Output) != `"done"` {
		t.Fatalf("output=%s", res.Output)
	}
	for _, n := range []string{"x0", "x1", "x2", "x3", "x4", "x5"} {
		if got := counters[n].Load(); got != 1 {
			t.Fatalf("step %s ran %d times", n, got)
		}
	}
}

func TestContinuationKeepsFailedStepCodeMessageAndRetryable(t *testing.T) {
	rt := chainRuntime(t, 10)
	mustRegisterStep(t, rt, "retry", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, &core.Error{Code: core.CodeUnavailable, Message: "temporal: step down", Retryable: true}
	}, runtime.StepDefaults{})
	mustRegisterStep(t, rt, "permanent", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, &core.Error{Code: core.CodeInvalidArgument, Message: "temporal: bad input", Retryable: false}
	}, runtime.StepDefaults{})
	var log execRunLog[struct {
		retry, perm *core.Error
	}]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		for _, name := range []string{"retry", "permanent"} {
			err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil)
			var pe *core.Error
			if !errors.As(err, &pe) {
				return nil, fmt.Errorf("step %s: %w", name, err)
			}
			if name == "retry" {
				rec.retry = pe
			} else {
				rec.perm = pe
			}
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		for _, name := range []string{"retry", "permanent"} {
			err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil)
			var pe *core.Error
			if !errors.As(err, &pe) {
				return nil, fmt.Errorf("after ckpt step %s: %w", name, err)
			}
			if name == "retry" {
				rec.retry = pe
			} else {
				rec.perm = pe
			}
		}
		return json.RawMessage(`"ok"`), nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	e1, e2 := mustExecRunLogEntry(t, &log, 0), mustExecRunLogEntry(t, &log, 1)
	for _, pair := range []struct{ a, b *core.Error }{{e1.retry, e2.retry}, {e1.perm, e2.perm}} {
		if pair.a.Code != pair.b.Code || pair.a.Message != pair.b.Message || pair.a.Retryable != pair.b.Retryable {
			t.Fatalf("mismatch %+v vs %+v", pair.a, pair.b)
		}
	}
	if e1.retry.Code != core.CodeUnavailable || !e1.retry.Retryable {
		t.Fatalf("retry %+v", e1.retry)
	}
	if e1.perm.Code != core.CodeInvalidArgument || e1.perm.Retryable {
		t.Fatalf("perm %+v", e1.perm)
	}
}

func TestContinuationNewIDAndRandMatchUninterruptedRun(t *testing.T) {
	runFlow := func(threshold int) (idRandCapture, int) {
		rt := chainRuntime(t, threshold)
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			var out idRandCapture
			for i := 0; i < 4; i++ {
				out.IDs = append(out.IDs, dc.NewID("k"))
				out.Rands = append(out.Rands, dc.Rand("r").Uint64())
			}
			if threshold > 0 {
				if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
					return nil, err
				}
			}
			for i := 0; i < 2; i++ {
				out.IDs = append(out.IDs, dc.NewID("k"))
			}
			return json.Marshal(out)
		})
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
		var got idRandCapture
		mustUnmarshal(t, res.Output, &got)
		return got, res.Execs
	}
	a, exA := runFlow(-1)
	b, exB := runFlow(10)
	if exA != 1 {
		t.Fatalf("no-continue execs=%d", exA)
	}
	if exB != 2 {
		t.Fatalf("continued execs=%d", exB)
	}
	assertDeepEqual(t, a, b)
	if len(a.IDs) != 6 || len(a.Rands) != 4 {
		t.Fatalf("ids=%d rands=%d", len(a.IDs), len(a.Rands))
	}
	allSame := true
	for _, r := range a.Rands {
		if r != a.Rands[0] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Fatal("rand draws all equal")
	}
}

func TestContinuationNewIDKeepsCountingAfterReplay(t *testing.T) {
	rt := chainRuntime(t, 10)
	var log execRunLog[[]string]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		want := []string{"k_1", "k_2", "k_3", "k_4", "k_5"}
		for i := 0; i < 3; i++ {
			*rec = append(*rec, dc.NewID("k"))
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			for i := 0; i < 2; i++ {
				*rec = append(*rec, dc.NewID("k"))
			}
			return nil, err
		}
		for i := 0; i < 2; i++ {
			*rec = append(*rec, dc.NewID("k"))
		}
		return json.Marshal(want)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 {
		t.Fatalf("Execs=%d", res.Execs)
	}
	for exec := 0; exec < 2; exec++ {
		got := log.at(exec)
		if got == nil {
			t.Fatalf("exec %d missing", exec)
		}
		want := []string{"k_1", "k_2", "k_3", "k_4", "k_5"}
		assertDeepEqual(t, want, *got)
	}
	var final []string
	mustUnmarshal(t, res.Output, &final)
	assertDeepEqual(t, []string{"k_1", "k_2", "k_3", "k_4", "k_5"}, final)
}

func TestContinuationFiredTimerIsNotWaitedAgain(t *testing.T) {
	rt := chainRuntime(t, 10)
	var obs0, obs1 timerListenerStats
	opts := defaultChainOpts()
	opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			attachTimerListeners(env, &obs0)
		},
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			attachTimerListeners(env, &obs1)
		},
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Sleep(time.Hour).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		if err := dc.Sleep(time.Hour).Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, opts)
	if res.Execs != 2 || string(res.Output) != `"done"` {
		t.Fatalf("execs=%d out=%s", res.Execs, res.Output)
	}
	if len(obs0.scheduled) != 1 || obs0.scheduled[0] != time.Hour || obs0.fired != 1 {
		t.Fatalf("exec0 timers %+v fired=%d", obs0.scheduled, obs0.fired)
	}
	if len(obs1.scheduled) != 1 || obs1.scheduled[0] != time.Hour {
		t.Fatalf("exec1 timers %+v", obs1.scheduled)
	}
}

func TestContinuationPendingTimerWaitsOnlyTheRemainingTime(t *testing.T) {
	T0 := chainFixedStart
	mk := func(t *testing.T, exec2Start time.Time, wantDuration time.Duration, wantTimers int) {
		t.Helper()
		rt := chainRuntime(t, 10)
		opts := defaultChainOpts()
		opts.startFor = func(exec int) time.Time {
			if exec == 0 {
				return T0
			}
			return exec2Start
		}
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			fut := dc.Sleep(2 * time.Hour)
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := fut.Get(nil); err != nil {
				return nil, err
			}
			return json.RawMessage(`"ok"`), nil
		})
		var obs timerListenerStats
		opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
			nil,
			func(_ int, env *testsuite.TestWorkflowEnvironment) {
				attachTimerListeners(env, &obs)
			},
		}
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, opts)
		if res.Execs != 2 {
			t.Fatalf("execs=%d", res.Execs)
		}
		if string(res.Output) != `"ok"` {
			t.Fatalf("output %s", res.Output)
		}
		if len(obs.scheduled) != wantTimers {
			t.Fatalf("timers %v want %d", obs.scheduled, wantTimers)
		}
		if wantTimers == 1 && obs.scheduled[0] != wantDuration {
			t.Fatalf("duration %v want %v", obs.scheduled[0], wantDuration)
		}
	}
	t.Run("remaining", func(t *testing.T) {
		mk(t, T0.Add(30*time.Minute), 90*time.Minute, 1)
	})
	t.Run("deadline_passed", func(t *testing.T) {
		mk(t, T0.Add(3*time.Hour), 0, 0)
	})
}

func TestContinuationNowReplaysRecordedValuesThenLiveTime(t *testing.T) {
	T0 := chainFixedStart
	rt := chainRuntime(t, 10)
	var log execRunLog[[]time.Time]
	opts := defaultChainOpts()
	opts.startFor = func(exec int) time.Time {
		if exec == 0 {
			return T0
		}
		return T0.Add(10 * time.Minute)
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		*rec = append(*rec, dc.Now(), dc.Now())
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		*rec = append(*rec, dc.Now())
		if err := dc.Sleep(time.Hour).Get(nil); err != nil {
			return nil, err
		}
		*rec = append(*rec, dc.Now(), dc.Now())
		return json.Marshal(rec)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	e1, e2 := mustExecRunLogEntry(t, &log, 0), mustExecRunLogEntry(t, &log, 1)
	if len(*e1) != 2 || len(*e2) != 5 {
		t.Fatalf("log e1=%d e2=%d", len(*e1), len(*e2))
	}
	if !(*e2)[0].Equal((*e1)[0]) || !(*e2)[1].Equal((*e1)[1]) {
		t.Fatalf("replay now mismatch %v %v vs %v %v", (*e2)[0], (*e2)[1], (*e1)[0], (*e1)[1])
	}
	wantLive := T0.Add(10 * time.Minute)
	if !(*e2)[2].Equal(wantLive) {
		t.Fatalf("live now3=%v want %v", (*e2)[2], wantLive)
	}
	wantAfterSleep := T0.Add(10*time.Minute + time.Hour)
	if !(*e2)[4].Equal(wantAfterSleep) {
		t.Fatalf("now5=%v want %v", (*e2)[4], wantAfterSleep)
	}
}

func TestContinuationSleepReplaysThenWaitsLive(t *testing.T) {
	rt := chainRuntime(t, 10)
	var obs timerListenerStats
	opts := defaultChainOpts()
	opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		nil,
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			attachTimerListeners(env, &obs)
		},
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Sleep(time.Minute).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		if err := dc.Sleep(2 * time.Hour).Get(nil); err != nil {
			return nil, err
		}
		t1 := dc.Now()
		if err := dc.Sleep(3 * time.Hour).Get(nil); err != nil {
			return nil, err
		}
		t2 := dc.Now()
		return json.Marshal(t2.Sub(t1).String())
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	if string(res.Output) != `"3h0m0s"` {
		t.Fatalf("delta %s", res.Output)
	}
	want := []time.Duration{2 * time.Hour, 3 * time.Hour}
	assertDeepEqual(t, want, obs.scheduled)
}

func TestContinuationSelectReplaysRecordedBranchEvenWhenAnotherFutureIsReady(t *testing.T) {
	T0 := chainFixedStart
	rt := chainRuntime(t, 10)
	opts := defaultChainOpts()
	opts.startFor = func(exec int) time.Time {
		if exec == 0 {
			return T0
		}
		return T0.Add(2 * time.Hour)
	}
	var log execRunLog[struct{ choice int }]
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		rec := log.begin()
		fast := dc.Sleep(time.Minute)
		slow := dc.Sleep(time.Hour)
		i, err := dc.Select(slow, fast)
		if err != nil {
			return nil, err
		}
		rec.choice = i
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		return json.Marshal(i)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	e0 := mustExecRunLogEntry(t, &log, 0)
	if e0.choice != 1 {
		t.Fatalf("exec0 choice=%d", e0.choice)
	}
	var out int
	mustUnmarshal(t, res.Output, &out)
	if out != 1 {
		t.Fatalf("output=%d", out)
	}
}
