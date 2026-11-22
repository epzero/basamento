package temporal

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

const gateCancelProbeWorkflow = "gate-cancel-probe"

type gateCancelProbeIn struct {
	Continuation *Continuation `json:"continuation,omitempty"`
	Timeout      time.Duration `json:"timeout,omitempty"`
}

type gateCancelProbeOut struct {
	GetCode     string      `json:"get_code"`
	GetMessage  string      `json:"get_message"`
	GateJournal GateJournal `json:"gate_journal"`
}

func registerGateCancelProbe(env *testsuite.TestWorkflowEnvironment, rt *Runtime) {
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in gateCancelProbeIn) (gateCancelProbeOut, error) {
		cctx, cancel := workflow.WithCancel(ctx)
		cancel()
		tenant := tenantFromWorkflow(ctx)
		hostIn := HostInput{RunID: "r1", Continuation: in.Continuation}
		dc := newDurableContext(cctx, rt, hostIn, tenant)
		if err := dc.registerGateUpdateHandler(); err != nil {
			return gateCancelProbeOut{}, err
		}
		spec := runtime.GateSpec{Name: "g", Timeout: in.Timeout}
		f := dc.Await(spec)
		var payload json.RawMessage
		getErr := f.Get(&payload)
		out := gateCancelProbeOut{GateJournal: dc.cont.Gates["g"]}
		if getErr != nil {
			var pe *core.Error
			if errors.As(getErr, &pe) {
				out.GetCode = string(pe.Code)
				out.GetMessage = pe.Message
			}
		}
		return out, nil
	}, workflow.RegisterOptions{Name: gateCancelProbeWorkflow})
}

func runGateCancelProbe(t *testing.T, in gateCancelProbeIn) gateCancelProbeOut {
	t.Helper()
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	registerGateCancelProbe(env, rt)
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(gateCancelProbeWorkflow, in)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var out gateCancelProbeOut
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestGateAwaitAfterCancelProbeJournalsEndedByCancel kills G18: awaitAfterCancel must
// journal EndedByCancel before the validator/replay path can observe it.
func TestGateAwaitAfterCancelProbeJournalsEndedByCancel(t *testing.T) {
	timeout := 3 * time.Minute
	out := runGateCancelProbe(t, gateCancelProbeIn{Timeout: timeout})
	if out.GetCode != string(core.CodeCancelled) || out.GetMessage != "temporal: gate cancelled" {
		t.Fatalf("get %q %q", out.GetCode, out.GetMessage)
	}
	j := out.GateJournal
	if j.Name != "g" || !j.EndedByCancel {
		t.Fatalf("journal %+v", j)
	}
	if j.TimeoutNs != timeout.Nanoseconds() || j.Deadline.IsZero() {
		t.Fatalf("timeout journal %+v", j)
	}
}

func TestGateAwaitAfterCancelOnResolvedContinuation(t *testing.T) {
	wantPayload := json.RawMessage(`"resolved-payload"`)
	cont := &Continuation{
		Gates: map[string]GateJournal{
			"g": {Name: "g", Resolved: true, Payload: wantPayload},
		},
	}
	cont.ensure()
	out := runGateCancelProbe(t, gateCancelProbeIn{Continuation: cont})
	if out.GetCode != string(core.CodeCancelled) || out.GetMessage != "temporal: gate cancelled" {
		t.Fatalf("get %q %q", out.GetCode, out.GetMessage)
	}
	j := out.GateJournal
	if !j.Resolved {
		t.Fatalf("Resolved lost: %+v", j)
	}
	if string(j.Payload) != string(wantPayload) {
		t.Fatalf("payload %+v want %s", j.Payload, wantPayload)
	}
	if !j.EndedByCancel {
		t.Fatalf("EndedByCancel not set: %+v", j)
	}
}
