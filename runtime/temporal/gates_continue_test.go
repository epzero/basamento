package temporal

import (
	"encoding/json"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func chainFillHistLenHooks(opts *chainOptions) {
	if opts.maxExec == 0 {
		opts.maxExec = 12
	}
	if len(opts.perExec) < opts.maxExec {
		n := make([]func(int, *testsuite.TestWorkflowEnvironment), opts.maxExec)
		copy(n, opts.perExec)
		opts.perExec = n
	}
}

func TestGateTimeoutSurvivesContinueAsNewWithRemainingTime(t *testing.T) {
	rt := chainRuntime(t, 10)
	var schedDur time.Duration
	var schedCount int
	opts := defaultChainOpts()
	opts.startFor = func(exec int) time.Time {
		if exec == 1 {
			return chainFixedStart.Add(10 * time.Hour)
		}
		return chainFixedStart
	}
	chainFillHistLenHooks(&opts)
	opts.perExec[1] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
		env.SetOnTimerScheduledListener(func(_ string, d time.Duration) {
			schedCount++
			schedDur = d
		})
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		g := dc.Await(runtime.GateSpec{Name: "approve", Timeout: 48 * time.Hour})
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		o := gateErrorOutcomeFrom(g.Get(nil))
		return json.Marshal(o)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "p"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	var out gateErrorOutcome
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != string(core.CodeTimeout) {
		t.Fatalf("code %q", out.Code)
	}
	if schedCount != 1 || schedDur != 38*time.Hour {
		t.Fatalf("timer count %d dur %v", schedCount, schedDur)
	}
}

func TestGateWithPassedDeadlineTimesOutWhenContinuedRunStarts(t *testing.T) {
	rt := chainRuntime(t, 10)
	var schedCount int
	opts := defaultChainOpts()
	opts.startFor = func(exec int) time.Time {
		if exec == 1 {
			return chainFixedStart.Add(49 * time.Hour)
		}
		return chainFixedStart
	}
	chainFillHistLenHooks(&opts)
	opts.perExec[1] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
		env.SetOnTimerScheduledListener(func(string, time.Duration) { schedCount++ })
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		g := dc.Await(runtime.GateSpec{Name: "g", Timeout: 48 * time.Hour})
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		o := gateErrorOutcomeFrom(g.Get(nil))
		return json.Marshal(o)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	if schedCount != 0 {
		t.Fatalf("timer scheduled %d", schedCount)
	}
	var out gateErrorOutcome
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != string(core.CodeTimeout) {
		t.Fatalf("code %q", out.Code)
	}
}

func TestGateResolvedBeforeContinueAsNewReturnsPayloadToReplayedFlow(t *testing.T) {
	rt := chainRuntime(t, 10)
	var first []byte
	opts := defaultChainOpts()
	chainFillHistLenHooks(&opts)
	opts.perExec[0] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
		env.RegisterDelayedCallback(func() {
			sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"ok"`))
		}, time.Second)
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Await(runtime.GateSpec{Name: "g"})
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		var p json.RawMessage
		if err := f.Get(&p); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		first = append(json.RawMessage(nil), p...)
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		var p2 json.RawMessage
		if err := dc.Await(runtime.GateSpec{Name: "g"}).Get(&p2); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		return p2, nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	if string(first) != `"ok"` || string(res.Output) != `"ok"` {
		t.Fatalf("first %s final %s", first, res.Output)
	}
}

func TestGateResolveAfterContinueAsNewIsAccepted(t *testing.T) {
	var upd *gateUpdateRecord
	var cbCount int
	opts := defaultChainOpts()
	chainFillHistLenHooks(&opts)
	opts.perExec[1] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
		env.RegisterDelayedCallback(func() {
			cbCount++
			upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"p"`))
		}, time.Minute)
	}
	rt := chainRuntime(t, 10)
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Await(runtime.GateSpec{Name: "g"})
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		var out json.RawMessage
		if err := f.Get(&out); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		return out, nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	if cbCount != 1 || upd == nil || !upd.accepted || !upd.completed || upd.completeErr != nil {
		t.Fatalf("update cbCount=%d upd=%+v", cbCount, upd)
	}
	if string(res.Output) != `"p"` {
		t.Fatalf("output %s", res.Output)
	}
}

func TestGateResolvedInEarlierExecutionAnswersConflictNotNotFound(t *testing.T) {
	t.Run("resolved", func(t *testing.T) {
		rt := chainRuntime(t, 10)
		var upd *gateUpdateRecord
		var cbCount int
		opts := defaultChainOpts()
		opts.histLenFor = func(exec int) int {
			if exec == 0 {
				return 25
			}
			return 10
		}
		chainFillHistLenHooks(&opts)
		opts.perExec[0] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.RegisterDelayedCallback(func() {
				sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"p"`))
			}, time.Second)
		}
		opts.perExec[1] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.RegisterDelayedCallback(func() {
				cbCount++
				upd = sendGateResolveUpdate(env, "u2", "g", json.RawMessage(`"q"`))
			}, time.Minute)
		}
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			f := dc.Await(runtime.GateSpec{Name: "g"})
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := f.Get(nil); err != nil {
				return json.Marshal(gateErrorOutcomeFrom(err))
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			// Keep the second execution alive until perExec sends the conflicting Update.
			if err := dc.Sleep(2 * time.Minute).Get(nil); err != nil {
				return nil, err
			}
			return json.RawMessage(`"done"`), nil
		})
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"}, opts)
		if res.Execs != 2 {
			t.Fatalf("execs %d", res.Execs)
		}
		if cbCount != 1 || upd == nil || upd.rejected == nil {
			t.Fatalf("cbCount=%d upd=%+v", cbCount, upd)
		}
		code, msg, ok := gateUpdateRejectInfo(upd.rejected)
		if !ok || code != string(core.CodeConflict) || msg != "temporal: the gate was already resolved" {
			t.Fatalf("code=%q msg=%q", code, msg)
		}
	})
	t.Run("timed_out", func(t *testing.T) {
		rt := chainRuntime(t, 10)
		var upd *gateUpdateRecord
		var cbCount int
		opts := defaultChainOpts()
		chainFillHistLenHooks(&opts)
		opts.perExec[1] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.RegisterDelayedCallback(func() {
				cbCount++
				upd = sendGateResolveUpdate(env, "u2", "g", json.RawMessage(`"q"`))
			}, time.Minute)
		}
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			f := dc.Await(runtime.GateSpec{Name: "g", Timeout: time.Second})
			if err := f.Get(nil); err != nil {
				out := gateErrorOutcomeFrom(err)
				if out.Code != string(core.CodeTimeout) {
					return json.Marshal(out)
				}
			}
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := dc.Sleep(2 * time.Minute).Get(nil); err != nil {
				return nil, err
			}
			return json.RawMessage(`"done"`), nil
		})
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"}, opts)
		if res.Execs != 2 {
			t.Fatalf("execs %d", res.Execs)
		}
		if cbCount != 1 || upd == nil || upd.rejected == nil {
			t.Fatalf("cbCount=%d upd=%+v", cbCount, upd)
		}
		code, msg, ok := gateUpdateRejectInfo(upd.rejected)
		if !ok || code != string(core.CodeConflict) || msg != "temporal: the gate wait already ended" {
			t.Fatalf("code=%q msg=%q", code, msg)
		}
	})
}

// GateJournal deadlines are honoured after Continue-as-New.
func TestGateCarriedAcrossContinueAsNewKeepsJournaledDeadline(t *testing.T) {
	t.Run("deadline_passed", func(t *testing.T) {
		rt := chainRuntime(t, 10)
		var upd *gateUpdateRecord
		var cbCount int
		var out gateErrorOutcome
		opts := defaultChainOpts()
		opts.startFor = func(exec int) time.Time {
			if exec == 1 {
				return chainFixedStart.Add(2 * time.Hour)
			}
			return chainFixedStart
		}
		chainFillHistLenHooks(&opts)
		opts.perExec[1] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.RegisterDelayedCallback(func() {
				cbCount++
				upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"p"`))
			}, time.Minute)
		}
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			_ = dc.Await(runtime.GateSpec{Name: "g", Timeout: time.Hour})
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := dc.Sleep(10 * time.Minute).Get(nil); err != nil {
				return nil, err
			}
			out = gateErrorOutcomeFrom(dc.Await(runtime.GateSpec{Name: "g"}).Get(nil))
			return json.Marshal(out)
		})
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"}, opts)
		if res.Execs != 2 {
			t.Fatalf("execs %d", res.Execs)
		}
		if cbCount != 1 || upd == nil || upd.rejected == nil {
			t.Fatalf("update %+v cb %d", upd, cbCount)
		}
		code, msg, ok := gateUpdateRejectInfo(upd.rejected)
		if !ok || code != string(core.CodeConflict) || msg != "temporal: the gate wait already ended" {
			t.Fatalf("reject code=%q msg=%q", code, msg)
		}
		if out.Code != string(core.CodeTimeout) {
			t.Fatalf("await %+v", out)
		}
	})
	t.Run("deadline_not_passed", func(t *testing.T) {
		rt := chainRuntime(t, 10)
		var upd *gateUpdateRecord
		opts := defaultChainOpts()
		opts.startFor = func(exec int) time.Time {
			if exec == 1 {
				return chainFixedStart.Add(10 * time.Minute)
			}
			return chainFixedStart
		}
		chainFillHistLenHooks(&opts)
		opts.perExec[1] = func(_ int, env *testsuite.TestWorkflowEnvironment) {
			env.RegisterDelayedCallback(func() {
				upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"p"`))
			}, time.Minute)
		}
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			_ = dc.Await(runtime.GateSpec{Name: "g", Timeout: time.Hour})
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := dc.Sleep(10 * time.Minute).Get(nil); err != nil {
				return nil, err
			}
			var p json.RawMessage
			if err := dc.Await(runtime.GateSpec{Name: "g"}).Get(&p); err != nil {
				return json.Marshal(gateErrorOutcomeFrom(err))
			}
			return p, nil
		})
		res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"}, opts)
		if res.Execs != 2 {
			t.Fatalf("execs %d", res.Execs)
		}
		if upd == nil || !upd.accepted || upd.rejected != nil {
			t.Fatalf("update %+v", upd)
		}
		if string(res.Output) != `"p"` {
			t.Fatalf("output %s", res.Output)
		}
	})
}
