package temporal

import (
	"encoding/json"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type gateJournalHostResult struct {
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func gateAwaitOutcomeHost(gate string) runtime.FlowHost {
	return func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var p json.RawMessage
		err := dc.Await(runtime.GateSpec{Name: gate}).Get(&p)
		if err != nil {
			rec := gateErrorOutcomeFrom(err)
			return json.Marshal(gateJournalHostResult{Code: rec.Code, Message: rec.Message})
		}
		return json.Marshal(gateJournalHostResult{Payload: p})
	}
}

func runContinuationHostWorkflow(t *testing.T, cont *Continuation, start time.Time, host runtime.FlowHost, before func(*testsuite.TestWorkflowEnvironment)) json.RawMessage {
	t.Helper()
	rt, env := testEnv(t)
	env.SetStartTime(start)
	mustRegisterFlow(t, rt, host)
	registerAcmeEnv(t, rt, env)
	if before != nil {
		before(env)
	}
	env.ExecuteWorkflow(HostWorkflowName, HostInput{
		Flow:         runtime.FlowRef{Name: "gj"},
		RunID:        "r1",
		Continuation: cont,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	var raw json.RawMessage
	if err := env.GetWorkflowResult(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustGateJournalResult(t *testing.T, raw json.RawMessage) gateJournalHostResult {
	t.Helper()
	var out gateJournalHostResult
	mustUnmarshal(t, raw, &out)
	return out
}

func idleSleepFlowHost() runtime.FlowHost {
	return func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Sleep(time.Hour).Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`"idle"`), nil
	}
}

func TestGateJournalResolvedReturnsPayloadWithoutTimer(t *testing.T) {
	var sched int
	cont := &Continuation{
		Gates: map[string]GateJournal{
			"g": {Name: "g", Resolved: true, Payload: json.RawMessage(`"p"`)},
		},
	}
	raw := runContinuationHostWorkflow(t, cont, chainFixedStart, gateAwaitOutcomeHost("g"), func(env *testsuite.TestWorkflowEnvironment) {
		env.SetOnTimerScheduledListener(func(string, time.Duration) { sched++ })
	})
	out := mustGateJournalResult(t, raw)
	if string(out.Payload) != `"p"` || out.Code != "" {
		t.Fatalf("out %+v", out)
	}
	if sched != 0 {
		t.Fatalf("timers scheduled %d", sched)
	}
}

func TestGateJournalEndedByTimeoutReturnsExactMessage(t *testing.T) {
	var sched int
	cont := &Continuation{
		Gates: map[string]GateJournal{
			"g": {
				Name:           "g",
				TimeoutNs:      time.Hour.Nanoseconds(),
				Deadline:       chainFixedStart.Add(time.Hour),
				EndedByTimeout: true,
			},
		},
	}
	raw := runContinuationHostWorkflow(t, cont, chainFixedStart, gateAwaitOutcomeHost("g"), func(env *testsuite.TestWorkflowEnvironment) {
		env.SetOnTimerScheduledListener(func(string, time.Duration) { sched++ })
	})
	out := mustGateJournalResult(t, raw)
	if out.Code != string(core.CodeTimeout) || out.Message != `temporal: gate "g" timed out` {
		t.Fatalf("out %+v", out)
	}
	if sched != 0 {
		t.Fatalf("timers scheduled %d", sched)
	}
}

func TestGateJournalEndedByCancelReturnsExactMessage(t *testing.T) {
	cont := &Continuation{
		Gates: map[string]GateJournal{
			"g": {Name: "g", Deadline: chainFixedStart, EndedByCancel: true},
		},
	}
	out := mustGateJournalResult(t, runContinuationHostWorkflow(t, cont, chainFixedStart, gateAwaitOutcomeHost("g"), nil))
	if out.Code != string(core.CodeCancelled) || out.Message != "temporal: gate cancelled" {
		t.Fatalf("out %+v", out)
	}
}

func TestGateJournalPendingDeadlinePastTimesOutImmediately(t *testing.T) {
	var sched int
	cont := &Continuation{
		Gates: map[string]GateJournal{
			"g": {
				Name:      "g",
				TimeoutNs: time.Hour.Nanoseconds(),
				Deadline:  chainFixedStart.Add(time.Hour),
			},
		},
	}
	out := mustGateJournalResult(t, runContinuationHostWorkflow(t, cont, chainFixedStart.Add(2*time.Hour), gateAwaitOutcomeHost("g"), func(env *testsuite.TestWorkflowEnvironment) {
		env.SetOnTimerScheduledListener(func(string, time.Duration) { sched++ })
	}))
	if out.Code != string(core.CodeTimeout) {
		t.Fatalf("out %+v", out)
	}
	if sched != 0 {
		t.Fatalf("timers scheduled %d", sched)
	}
}

func TestGateJournalPendingFutureDeadlineSchedulesTimerAndAcceptsUpdate(t *testing.T) {
	deadline := chainFixedStart.Add(time.Hour)
	var schedDur time.Duration
	var schedCount int
	var upd *gateUpdateRecord
	cont := &Continuation{
		Gates: map[string]GateJournal{
			"g": {
				Name:      "g",
				TimeoutNs: time.Hour.Nanoseconds(),
				Deadline:  deadline,
			},
		},
	}
	raw := runContinuationHostWorkflow(t, cont, chainFixedStart, gateAwaitOutcomeHost("g"), func(env *testsuite.TestWorkflowEnvironment) {
		env.SetOnTimerScheduledListener(func(_ string, dur time.Duration) {
			schedCount++
			schedDur = dur
		})
		env.RegisterDelayedCallback(func() {
			upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"p"`))
		}, time.Minute)
	})
	out := mustGateJournalResult(t, raw)
	if schedCount != 1 || schedDur != time.Hour {
		t.Fatalf("timer count=%d dur=%v", schedCount, schedDur)
	}
	if upd == nil || !upd.accepted || upd.rejected != nil {
		t.Fatalf("update %+v", upd)
	}
	if string(out.Payload) != `"p"` {
		t.Fatalf("out %+v", out)
	}
}

func TestGateJournalUpdateValidators(t *testing.T) {
	cases := []struct {
		name    string
		cont    *Continuation
		wantCod core.Code
		wantMsg string
	}{
		{
			name: "resolved_conflict",
			cont: &Continuation{Gates: map[string]GateJournal{
				"g": {Name: "g", Resolved: true, Payload: json.RawMessage(`"p"`)},
			}},
			wantCod: core.CodeConflict,
			wantMsg: "temporal: the gate was already resolved",
		},
		{
			name: "timeout_conflict",
			cont: &Continuation{Gates: map[string]GateJournal{
				"g": {Name: "g", EndedByTimeout: true, Deadline: chainFixedStart},
			}},
			wantCod: core.CodeConflict,
			wantMsg: "temporal: the gate wait already ended",
		},
		{
			name: "cancel_conflict",
			cont: &Continuation{Gates: map[string]GateJournal{
				"g": {Name: "g", EndedByCancel: true, Deadline: chainFixedStart},
			}},
			wantCod: core.CodeConflict,
			wantMsg: "temporal: the gate wait already ended",
		},
		{
			name:    "not_found",
			cont:    &Continuation{Gates: map[string]GateJournal{}},
			wantCod: core.CodeNotFound,
			wantMsg: "temporal: no gate with that name on the run",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upd *gateUpdateRecord
			rt, env := testEnv(t)
			env.SetStartTime(chainFixedStart)
			mustRegisterFlow(t, rt, idleSleepFlowHost())
			registerAcmeEnv(t, rt, env)
			env.RegisterDelayedCallback(func() {
				upd = sendGateResolveUpdate(env, "u", "g", json.RawMessage(`"x"`))
			}, time.Millisecond)
			env.ExecuteWorkflow(HostWorkflowName, HostInput{
				Flow:         runtime.FlowRef{Name: "gj"},
				RunID:        "r1",
				Continuation: tc.cont,
			})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatalf("workflow: %v", err)
			}
			if upd == nil || upd.rejected == nil {
				t.Fatal("expected reject")
			}
			code, msg, ok := gateUpdateRejectInfo(upd.rejected)
			if !ok || code != string(tc.wantCod) || msg != tc.wantMsg {
				t.Fatalf("code=%q msg=%q", code, msg)
			}
		})
	}
}
