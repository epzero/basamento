package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stretchr/testify/mock"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/temporal"
)

const bddChainMaxExecutions = 100

// Modeled server history length: fixed workflow overhead plus six history events per completed activity (step or emit).
const bddHistBase = 3
const bddHistPerActivity = 6

const bddStepActivity = "basamento-step"

type flowHarnessMeta struct {
	slowStep  string
	orderStep string
}

type gateResolveArm struct {
	name                 string
	payload              json.RawMessage
	requireContinuations int
	outcome              gateUpdateOutcome
	callbackFired        bool
	continuesAtFire      int
}

type gateUpdateOutcome struct {
	accepted  bool
	rejected  error
	completed bool
}

type chainRunConfig struct {
	tenant core.TenantID
	flow   string
	runID  string
	gate   *gateResolveArm
}

type pendingGateRun struct {
	flow          string
	runID         string
	gate          string
	afterContinue bool
}

func decodeContinueInput(err error, dc *recordingConverter) (temporal.HostInput, bool) {
	var wf *sdktemporal.WorkflowExecutionError
	if !errors.As(err, &wf) {
		return temporal.HostInput{}, false
	}
	var can *workflow.ContinueAsNewError
	if !errors.As(wf.Unwrap(), &can) {
		return temporal.HostInput{}, false
	}
	if dc == nil {
		return temporal.HostInput{}, false
	}
	var in temporal.HostInput
	if decodeErr := dc.FromPayloads(can.Input, &in); decodeErr != nil {
		return temporal.HostInput{}, false
	}
	return in, true
}

func (w *temporalWorld) newExecutionEnv(slowStep string) (*testsuite.TestWorkflowEnvironment, *recordingConverter, error) {
	env := w.suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Hour)
	env.SetWorkflowRunTimeout(0)
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
	env.SetContextPropagators([]workflow.ContextPropagator{temporal.TenantPropagator{}})
	dataConv := &recordingConverter{
		inner:    w.rt.DataConverter(),
		enc:      w.enc,
		tenant:   w.activeTenant,
		recorded: &w.history,
		faultMode: func() string {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.faultMode
		},
		faultKey: func() string {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.faultKey
		},
		faultInjected: &w.faultInjected,
	}
	env.SetDataConverter(dataConv)
	if err := w.rt.Register(env); err != nil {
		return nil, nil, err
	}
	if slowStep != "" {
		slow := slowStep
		env.OnActivity(bddStepActivity, mock.Anything, mock.MatchedBy(func(in temporal.StepActivityInput) bool {
			return in.Name != slow
		})).Return(func(ctx context.Context, in temporal.StepActivityInput) (json.RawMessage, error) {
			return w.callStep(ctx, in.Name, in.In)
		})
		env.OnActivity(bddStepActivity, mock.Anything, mock.MatchedBy(func(in temporal.StepActivityInput) bool {
			return in.Name == slow
		})).After(time.Minute).Return(func(ctx context.Context, in temporal.StepActivityInput) (json.RawMessage, error) {
			return w.callStep(ctx, in.Name, in.In)
		})
	}
	return env, dataConv, nil
}

func (w *temporalWorld) setTenantOnEnv(env *testsuite.TestWorkflowEnvironment, tenant core.TenantID) {
	p, _ := converter.GetDefaultDataConverter().ToPayload(string(tenant))
	env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{"basamento-tenant": p}})
}

func (w *temporalWorld) armGateCallback(env *testsuite.TestWorkflowEnvironment, arm *gateResolveArm, continuations *int) {
	env.RegisterDelayedCallback(func() {
		arm.callbackFired = true
		arm.continuesAtFire = *continuations
		env.UpdateWorkflow(temporal.ResolveGateUpdate, arm.name, &testsuite.TestUpdateCallback{
			OnAccept: func() { arm.outcome.accepted = true },
			OnReject: func(err error) {
				arm.outcome.rejected = err
			},
			OnComplete: func(any, error) {
				arm.outcome.completed = true
			},
		}, temporal.GateResolution{Gate: arm.name, Payload: append(json.RawMessage(nil), arm.payload...)})
	}, time.Second)
}

func (w *temporalWorld) runChain(cfg chainRunConfig) error {
	if err := w.ensureRuntime(); err != nil {
		return err
	}
	if err := w.ensureNamedFlow(cfg.flow); err != nil {
		return err
	}
	w.mu.Lock()
	w.activeTenant = cfg.tenant
	w.faultInjected = false
	w.bddOutbox().wantTenant = cfg.tenant
	w.bddOutbox().wantRun = runtime.RunRef{RunID: cfg.runID, TenantID: cfg.tenant}
	w.continuations = 0
	w.stepBDoneExec = -1
	w.continueFromExec = -1
	meta := w.flowHarness[cfg.flow]
	w.mu.Unlock()
	if w.dataConv != nil {
		w.dataConv.tenant = cfg.tenant
	}
	in := temporal.HostInput{Flow: runtime.FlowRef{Name: cfg.flow}, RunID: cfg.runID}
	continuations := 0
	w.mu.Lock()
	w.orderProofStep = meta.orderStep
	w.mu.Unlock()
	for exec := 0; exec < bddChainMaxExecutions; exec++ {
		w.execActivityDone.Store(0)
		w.mu.Lock()
		w.currentExecIndex = exec
		w.mu.Unlock()
		env, dataConv, err := w.newExecutionEnv(meta.slowStep)
		if err != nil {
			return err
		}
		w.env = env
		w.dataConv = dataConv
		w.setTenantOnEnv(env, cfg.tenant)
		// The Emit activity derives the run id from the scheduling workflow id,
		// so the test client starts with the run id as the workflow id.
		env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: cfg.runID})
		if cfg.gate != nil {
			w.armGateCallback(env, cfg.gate, &continuations)
		}
		env.ExecuteWorkflow(temporal.HostWorkflowName, in)
		err = env.GetWorkflowError()
		if err == nil {
			w.mu.Lock()
			w.lastErr = nil
			w.mu.Unlock()
			if resErr := env.GetWorkflowResult(&w.lastOutput); resErr != nil {
				w.mu.Lock()
				w.lastErr = resErr
				w.mu.Unlock()
				return resErr
			}
			return nil
		}
		next, ok := decodeContinueInput(err, dataConv)
		if !ok {
			w.mu.Lock()
			w.lastErr = err
			w.mu.Unlock()
			return nil
		}
		continuations++
		w.mu.Lock()
		w.continuations = continuations
		w.continueFromExec = exec
		w.mu.Unlock()
		in = next
	}
	return fmt.Errorf("temporal bdd: exceeded %d continue-as-new executions", bddChainMaxExecutions)
}

// checkpoint sets modeled history length then calls the durable Checkpoint.
func (w *temporalWorld) checkpoint(dc runtime.DurableContext) error {
	n := w.execActivityDone.Load()
	w.env.SetCurrentHistoryLength(bddHistBase + bddHistPerActivity*int(n))
	return dc.Checkpoint(runtime.SnapshotRef{})
}

func stepRange(from, to string) ([]string, error) {
	prefix := strings.TrimRight(from, "0123456789")
	var start, end int
	if _, err := fmt.Sscanf(from, prefix+"%d", &start); err != nil {
		return nil, err
	}
	if _, err := fmt.Sscanf(to, prefix+"%d", &end); err != nil {
		return nil, err
	}
	if end < start {
		return nil, fmt.Errorf("invalid step range")
	}
	out := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		out = append(out, fmt.Sprintf("%s%d", prefix, i))
	}
	return out, nil
}

func stepRangeCount(from, to string) (int, error) {
	names, err := stepRange(from, to)
	if err != nil {
		return 0, err
	}
	return len(names), nil
}

func eventsFromFlowLiteral(text string) []string {
	text = strings.ReplaceAll(text, `"`, "")
	text = strings.ReplaceAll(text, " and ", ",")
	return quotedEventTypes(text)
}

func quotedEventTypes(list string) []string {
	list = strings.TrimSpace(list)
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" && p != "and" {
			out = append(out, p)
		}
	}
	return out
}
