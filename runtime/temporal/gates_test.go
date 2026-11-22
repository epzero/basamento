package temporal

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type gateErrorOutcome struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func gateErrorOutcomeFrom(err error) gateErrorOutcome {
	if err == nil {
		return gateErrorOutcome{}
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return gateErrorOutcome{Code: string(pe.Code), Message: pe.Message}
	}
	return gateErrorOutcome{Code: "unknown", Message: err.Error()}
}

func TestGateResolveReturnsPayload(t *testing.T) {
	rt, env := testEnv(t)
	var out json.RawMessage
	var upd *gateUpdateRecord
	var cbCount int
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Await(runtime.GateSpec{Name: "approve"})
		env.RegisterDelayedCallback(func() {
			cbCount++
			upd = sendGateResolveUpdate(env, "u1", "approve", json.RawMessage(`"yes"`))
		}, time.Second)
		if err := f.Get(&out); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		return out, nil
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if cbCount != 1 {
		t.Fatalf("callback count %d", cbCount)
	}
	if upd == nil || !upd.accepted || !upd.completed || upd.completeErr != nil {
		t.Fatalf("update %+v", upd)
	}
	if string(out) != `"yes"` {
		t.Fatalf("payload %s", out)
	}
}

func TestGateResolveUnknownNameIsNotFound(t *testing.T) {
	rt, env := testEnv(t)
	var upd *gateUpdateRecord
	var cbCount int
	env.RegisterDelayedCallback(func() {
		cbCount++
		upd = sendGateResolveUpdate(env, "u", "missing", json.RawMessage(`"p"`))
	}, time.Minute)
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		// Block until the test-env delayed callback can run.
		if err := dc.Sleep(2 * time.Minute).Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if cbCount != 1 {
		t.Fatalf("callback count %d", cbCount)
	}
	if upd == nil || upd.rejected == nil {
		t.Fatal("expected reject")
	}
	code, msg, ok := gateUpdateRejectInfo(upd.rejected)
	if !ok || code != string(core.CodeNotFound) || msg != "temporal: no gate with that name on the run" {
		t.Fatalf("reject code=%q msg=%q", code, msg)
	}
}

func TestGateResolveTwiceIsConflict(t *testing.T) {
	rt, env := testEnv(t)
	var upd1, upd2 *gateUpdateRecord
	var got json.RawMessage
	var cbCount int
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Await(runtime.GateSpec{Name: "g"})
		env.RegisterDelayedCallback(func() {
			cbCount++
			upd1 = sendGateResolveUpdate(env, "1", "g", json.RawMessage(`"first"`))
			upd2 = sendGateResolveUpdate(env, "2", "g", json.RawMessage(`"second"`))
		}, time.Second)
		if err := f.Get(&got); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		// Let the second Update reject callback run before the host returns.
		if err := dc.Sleep(time.Millisecond).Get(nil); err != nil {
			return nil, err
		}
		return got, nil
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if cbCount != 1 {
		t.Fatalf("callback count %d", cbCount)
	}
	if upd1 == nil || !upd1.accepted || !upd1.completed || upd1.completeErr != nil {
		t.Fatalf("update1 %+v", upd1)
	}
	if upd2 == nil || upd2.rejected == nil {
		t.Fatal("expected second reject")
	}
	code, msg, ok := gateUpdateRejectInfo(upd2.rejected)
	if !ok || code != string(core.CodeConflict) || msg != "temporal: the gate was already resolved" {
		t.Fatalf("reject code=%q msg=%q", code, msg)
	}
	if string(got) != `"first"` {
		t.Fatalf("payload %s", got)
	}
}

func TestGateResolveAfterTimeoutIsConflict(t *testing.T) {
	rt, env := testEnv(t)
	var settle gateErrorOutcome
	var upd *gateUpdateRecord
	var cbCount int
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		settle = gateErrorOutcomeFrom(dc.Await(runtime.GateSpec{Name: "g", Timeout: time.Second}).Get(nil))
		env.RegisterDelayedCallback(func() {
			cbCount++
			upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"p"`))
		}, 2*time.Second)
		// Stay alive until the late resolve attempt runs.
		if err := dc.Sleep(3 * time.Second).Get(nil); err != nil {
			return nil, err
		}
		return json.Marshal(settle)
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if cbCount != 1 {
		t.Fatalf("callback count %d", cbCount)
	}
	if settle.Code != string(core.CodeTimeout) {
		t.Fatalf("await settle code %q", settle.Code)
	}
	if upd == nil || upd.rejected == nil {
		t.Fatal("expected reject")
	}
	code, msg, ok := gateUpdateRejectInfo(upd.rejected)
	if !ok || code != string(core.CodeConflict) || msg != "temporal: the gate wait already ended" {
		t.Fatalf("reject code=%q msg=%q", code, msg)
	}
}

func TestGateTimeoutSettlesTimeoutAndLeavesNoTimerBehind(t *testing.T) {
	rt, env := testEnv(t)
	var out gateErrorOutcome
	var scheduled, fired, canceled int
	env.SetOnTimerScheduledListener(func(string, time.Duration) { scheduled++ })
	env.SetOnTimerFiredListener(func(string) { fired++ })
	env.SetOnTimerCanceledListener(func(string) { canceled++ })
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		out = gateErrorOutcomeFrom(dc.Await(runtime.GateSpec{Name: "g", Timeout: time.Second}).Get(nil))
		return json.Marshal(out)
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if out.Code != string(core.CodeTimeout) || out.Message != `temporal: gate "g" timed out` {
		t.Fatalf("out %+v", out)
	}
	if scheduled != 1 || fired != 1 || canceled != 0 {
		t.Fatalf("timers sched=%d fired=%d canceled=%d", scheduled, fired, canceled)
	}
}

func TestGateResolveCancelsItsTimerAndDoesNotTimeOut(t *testing.T) {
	rt, env := testEnv(t)
	var got json.RawMessage
	var upd *gateUpdateRecord
	var scheduled, fired, canceled int
	env.SetOnTimerScheduledListener(func(string, time.Duration) { scheduled++ })
	env.SetOnTimerFiredListener(func(string) { fired++ })
	env.SetOnTimerCanceledListener(func(string) { canceled++ })
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Await(runtime.GateSpec{Name: "g", Timeout: time.Hour})
		env.RegisterDelayedCallback(func() {
			upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"p"`))
		}, time.Second)
		if err := f.Get(&got); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		// Advance simulated time so timer cancel/fire listeners run.
		if err := dc.Sleep(2 * time.Second).Get(nil); err != nil {
			return nil, err
		}
		return got, nil
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if string(got) != `"p"` {
		t.Fatalf("payload %s", got)
	}
	if scheduled != 2 || fired != 1 || canceled != 1 {
		t.Fatalf("timers sched=%d fired=%d canceled=%d", scheduled, fired, canceled)
	}
	if upd == nil || !upd.accepted {
		t.Fatalf("update %+v", upd)
	}
}

func TestGateCancelWhileBlockedOnGetRejectsUpdate(t *testing.T) {
	rt, env := testEnv(t)
	var g1 gateErrorOutcome
	var upd *gateUpdateRecord
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
		upd = sendGateResolveUpdate(env, "u", "g1", json.RawMessage(`"p"`))
	}, time.Second)
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Await(runtime.GateSpec{Name: "g1", Timeout: time.Hour})
		g1 = gateErrorOutcomeFrom(f.Get(nil))
		if err := dc.Sleep(10 * time.Millisecond).Get(nil); err != nil {
			return nil, err
		}
		return json.Marshal(g1)
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	want := gateErrorOutcome{Code: string(core.CodeCancelled), Message: "temporal: gate cancelled"}
	if g1 != want {
		t.Fatalf("gate %+v", g1)
	}
	if upd == nil || upd.rejected == nil {
		t.Fatalf("update %+v", upd)
	}
	code, msg, ok := gateUpdateRejectInfo(upd.rejected)
	if !ok || code != string(core.CodeConflict) || msg != "temporal: the gate wait already ended" {
		t.Fatalf("reject code=%q msg=%q", code, msg)
	}
}

func TestGateCancellationSettlesPendingGatesCancelled(t *testing.T) {
	rt, env := testEnv(t)
	var g1, g2 gateErrorOutcome
	var cbCount int
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f1 := dc.Await(runtime.GateSpec{Name: "g1", Timeout: time.Hour})
		f2 := dc.Await(runtime.GateSpec{Name: "g2"})
		env.RegisterDelayedCallback(func() {
			cbCount++
			env.CancelWorkflow()
		}, time.Second)
		g1 = gateErrorOutcomeFrom(f1.Get(nil))
		g2 = gateErrorOutcomeFrom(f2.Get(nil))
		if err := dc.Sleep(time.Millisecond).Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if cbCount != 1 {
		t.Fatalf("callback count %d", cbCount)
	}
	want := gateErrorOutcome{Code: string(core.CodeCancelled), Message: "temporal: gate cancelled"}
	if g1 != want || g2 != want {
		t.Fatalf("g1 %+v g2 %+v", g1, g2)
	}
}

func TestGateAwaitAfterCancellationReturnsCancelledAndRunEnds(t *testing.T) {
	rt, env := testEnv(t)
	var out gateErrorOutcome
	var cbCount int
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		env.RegisterDelayedCallback(func() {
			cbCount++
			env.CancelWorkflow()
		}, time.Second)
		// Cancel fires at +1s; advance simulated time until cancel is delivered.
		if err := dc.Sleep(2 * time.Second).Get(nil); err != nil {
			if core.CodeOf(err) != core.CodeCancelled {
				return nil, err
			}
		}
		out = gateErrorOutcomeFrom(dc.Await(runtime.GateSpec{Name: "g"}).Get(nil))
		return json.Marshal(out)
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if cbCount != 1 {
		t.Fatalf("callback count %d", cbCount)
	}
	want := gateErrorOutcome{Code: string(core.CodeCancelled), Message: "temporal: gate cancelled"}
	if out != want {
		t.Fatalf("out %+v", out)
	}
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
}

func TestGateSameNameTwiceSharesOneWait(t *testing.T) {
	rt, env := testEnv(t)
	var upd1, upd2 *gateUpdateRecord
	var p1, p2 json.RawMessage
	var cbCount int
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		spec := runtime.GateSpec{Name: "g", Timeout: 5 * time.Second}
		f1 := dc.Await(spec)
		f2 := dc.Await(spec)
		env.RegisterDelayedCallback(func() {
			cbCount++
			upd1 = sendGateResolveUpdate(env, "1", "g", json.RawMessage(`"p"`))
		}, time.Second)
		env.RegisterDelayedCallback(func() {
			upd2 = sendGateResolveUpdate(env, "2", "g", json.RawMessage(`"q"`))
		}, 2*time.Second)
		if err := f1.Get(&p1); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		if err := dc.Sleep(3 * time.Second).Get(nil); err != nil {
			return nil, err
		}
		if err := f2.Get(&p2); err != nil {
			return json.Marshal(gateErrorOutcomeFrom(err))
		}
		return json.Marshal([]json.RawMessage{p1, p2})
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if cbCount != 1 || upd1 == nil || !upd1.accepted || upd2 == nil || upd2.rejected == nil {
		t.Fatalf("updates upd1=%+v upd2=%+v cb=%d", upd1, upd2, cbCount)
	}
	code, msg, ok := gateUpdateRejectInfo(upd2.rejected)
	if !ok || code != string(core.CodeConflict) {
		t.Fatalf("reject code=%q msg=%q", code, msg)
	}
	if string(p1) != `"p"` || string(p2) != `"p"` {
		t.Fatalf("p1=%s p2=%s", p1, p2)
	}
}

func TestSelectOverGateAndTimer(t *testing.T) {
	t.Run("gate_first", func(t *testing.T) {
		rt, env := testEnv(t)
		var idx int
		var payload json.RawMessage
		var cbCount int
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			g := dc.Await(runtime.GateSpec{Name: "g"})
			tm := dc.Sleep(time.Hour)
			env.RegisterDelayedCallback(func() {
				cbCount++
				sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"gp"`))
			}, time.Second)
			var selErr error
			idx, selErr = dc.Select(tm, g)
			if selErr != nil {
				return json.Marshal(gateErrorOutcomeFrom(selErr))
			}
			if idx == 1 {
				if err := g.Get(&payload); err != nil {
					return json.Marshal(gateErrorOutcomeFrom(err))
				}
			}
			return json.Marshal(struct {
				Idx     int             `json:"idx"`
				Payload json.RawMessage `json:"payload"`
			}{Idx: idx, Payload: payload})
		})
		registerAcmeEnv(t, rt, env)
		env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("workflow: %v", err)
		}
		if cbCount != 1 {
			t.Fatalf("callback count %d", cbCount)
		}
		var out struct {
			Idx     int             `json:"idx"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := env.GetWorkflowResult(&out); err != nil {
			t.Fatal(err)
		}
		if out.Idx != 1 || string(out.Payload) != `"gp"` {
			t.Fatalf("out %+v", out)
		}
	})
	t.Run("timer_first", func(t *testing.T) {
		rt, env := testEnv(t)
		var idx int
		var payload json.RawMessage
		var cbCount int
		mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
			g := dc.Await(runtime.GateSpec{Name: "g"})
			tm := dc.Sleep(time.Second)
			env.RegisterDelayedCallback(func() {
				cbCount++
				sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"gp"`))
			}, time.Hour)
			var selErr error
			idx, selErr = dc.Select(tm, g)
			if selErr != nil {
				return json.Marshal(gateErrorOutcomeFrom(selErr))
			}
			if err := g.Get(&payload); err != nil {
				return json.Marshal(gateErrorOutcomeFrom(err))
			}
			return json.Marshal(struct {
				Idx     int             `json:"idx"`
				Payload json.RawMessage `json:"payload"`
			}{Idx: idx, Payload: payload})
		})
		registerAcmeEnv(t, rt, env)
		env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "g"}, RunID: "r1"})
		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("workflow: %v", err)
		}
		if cbCount != 1 {
			t.Fatalf("callback count %d", cbCount)
		}
		var out struct {
			Idx     int             `json:"idx"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := env.GetWorkflowResult(&out); err != nil {
			t.Fatal(err)
		}
		if out.Idx != 0 || string(out.Payload) != `"gp"` {
			t.Fatalf("out %+v", out)
		}
	})
}
