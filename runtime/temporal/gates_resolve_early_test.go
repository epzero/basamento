package temporal

import (
	"encoding/json"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// pendingGateJournal is a pending gate on a continued run. The deadline is far
// enough that a broken handler still eventually times out in mock time (after the
// 2 minute Sleep, ~8 minutes remain) instead of hanging until the test timeout.
func pendingGateJournal() GateJournal {
	return GateJournal{
		Name:      "g",
		TimeoutNs: (10 * time.Minute).Nanoseconds(),
		Deadline:  chainFixedStart.Add(10 * time.Minute),
	}
}

// pendingGateAfterContinue is a continuation left by exec 0 (Await, step, Checkpoint)
// with a gate still pending before the continued run re-awaits it.
func pendingGateAfterContinue() *Continuation {
	return &Continuation{
		Steps: map[string]StepOutcome{
			"s": {Result: json.RawMessage(`"ok"`)},
		},
		Gates: map[string]GateJournal{
			"g": pendingGateJournal(),
		},
		Checkpoints: 1,
	}
}

func sleepThenAwaitGateHost() runtime.FlowHost {
	return func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		// Live on a continued run: blocks the main coroutine so the Update handler can
		// run before the flow calls Await again (plain replay does not yield here).
		if err := dc.Sleep(2 * time.Minute).Get(nil); err != nil {
			return nil, err
		}
		var p json.RawMessage
		if err := dc.Await(runtime.GateSpec{Name: "g"}).Get(&p); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		return p, nil
	}
}

func TestGateResolveBeforeAwaitAfterContinueAsNewIsKept(t *testing.T) {
	var upd, upd2 *gateUpdateRecord
	raw := runContinuationHostWorkflow(t, pendingGateAfterContinue(), chainFixedStart, sleepThenAwaitGateHost(), func(env *testsuite.TestWorkflowEnvironment) {
		env.SetTestTimeout(30 * time.Second)
		env.RegisterDelayedCallback(func() {
			upd = sendGateResolveUpdate(env, "u1", "g", json.RawMessage(`"early"`))
		}, time.Minute)
		env.RegisterDelayedCallback(func() {
			upd2 = sendGateResolveUpdate(env, "u2", "g", json.RawMessage(`"late"`))
		}, 90*time.Second)
	})
	if upd == nil || !upd.accepted || upd.rejected != nil || !upd.completed || upd.completeErr != nil {
		t.Fatalf("first resolve %+v", upd)
	}
	if string(raw) != `"early"` {
		t.Fatalf("payload %s", raw)
	}
	if upd2 == nil || upd2.rejected == nil {
		t.Fatalf("second resolve %+v", upd2)
	}
	code, msg, ok := gateUpdateRejectInfo(upd2.rejected)
	if !ok || code != string(core.CodeConflict) || msg != "temporal: the gate was already resolved" {
		t.Fatalf("second reject code=%q msg=%q", code, msg)
	}
}

func TestGateResolveBeforeAwaitCheckpointCarriesResolved(t *testing.T) {
	rt := chainRuntime(t, 5)
	var upd *gateUpdateRecord
	var hookEnv *testsuite.TestWorkflowEnvironment
	opts := defaultChainOpts()
	opts.maxExec = 2
	opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		func(_ int, env *testsuite.TestWorkflowEnvironment) {
			hookEnv = env
			env.SetTestTimeout(30 * time.Second)
			env.RegisterDelayedCallback(func() {
				upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"early"`))
			}, time.Minute)
		},
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Sleep(2 * time.Minute).Get(nil); err != nil {
			return nil, err
		}
		var p json.RawMessage
		if err := dc.Await(runtime.GateSpec{Name: "g"}).Get(&p); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		hookEnv.SetCurrentHistoryLength(5)
		return nil, dc.Checkpoint(runtime.SnapshotRef{})
	})
	cont := &Continuation{
		Gates: map[string]GateJournal{"g": pendingGateJournal()},
	}
	res := runChain(t, rt, HostInput{
		Flow:         runtime.FlowRef{Name: "g"},
		RunID:        "r1",
		Continuation: cont,
	}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	if upd == nil || !upd.accepted || upd.rejected != nil {
		t.Fatalf("resolve %+v", upd)
	}
	if !isContinueAsNewWorkflowErr(res.PerErr[0]) {
		t.Fatalf("exec0 err %v", res.PerErr[0])
	}
	j, ok := res.Inputs[1].Continuation.Gates["g"]
	if !ok || !j.Resolved || string(j.Payload) != `"early"` {
		t.Fatalf("continued gate journal %+v ok=%v", j, ok)
	}
}
