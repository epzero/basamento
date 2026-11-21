package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

var chainFixedStart = time.Unix(1_700_000_000, 0).UTC()

// chainOptions configures runChain. perExec[i] runs for execution index i
// immediately before ExecuteWorkflow, after the environment is set up, so hooks
// can add listeners, mocks and delayed callbacks. histLenFor fixes history
// length for the whole execution; a flow that needs a different length between
// two Checkpoint calls can capture env in a perExec hook and call
// env.SetCurrentHistoryLength from inside the flow.
type chainOptions struct {
	histLen        int
	histLenFor     func(exec int) int
	perExec        []func(exec int, env *testsuite.TestWorkflowEnvironment)
	tenant         string
	maxExec        int
	startFor       func(exec int) time.Time
	allowFlowError bool
	afterExec      func(exec int, env *testsuite.TestWorkflowEnvironment)
}

type chainResult struct {
	Output    json.RawMessage
	Err       error
	Execs     int
	PerOutput []json.RawMessage
	PerErr    []error
	Inputs    []HostInput
}

func defaultChainOpts() chainOptions {
	return chainOptions{histLen: 25, tenant: "acme", maxExec: 12}
}

func runChain(t *testing.T, rt *Runtime, in HostInput, opts chainOptions) chainResult {
	t.Helper()
	if opts.tenant == "" {
		opts.tenant = "acme"
	}
	if opts.maxExec == 0 {
		opts.maxExec = 12
	}
	cur := in
	var res chainResult
	res.Inputs = append(res.Inputs, cur)
	for res.Execs < opts.maxExec {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.SetTestTimeout(time.Hour)
		env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
		env.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
		conv := &clientConverter{inner: rt.DataConverter(), tenant: core.TenantID(opts.tenant)}
		env.SetDataConverter(conv)
		setTestTenantHeader(env, opts.tenant)
		start := chainFixedStart
		if opts.startFor != nil {
			start = opts.startFor(res.Execs)
		}
		env.SetStartTime(start)
		hl := opts.histLen
		if opts.histLenFor != nil {
			hl = opts.histLenFor(res.Execs)
		}
		env.SetCurrentHistoryLength(hl)
		if err := rt.Register(env); err != nil {
			t.Fatal(err)
		}
		if opts.perExec != nil && res.Execs < len(opts.perExec) && opts.perExec[res.Execs] != nil {
			opts.perExec[res.Execs](res.Execs, env)
		}
		env.ExecuteWorkflow(HostWorkflowName, cur)
		if opts.afterExec != nil {
			opts.afterExec(res.Execs, env)
		}
		res.Execs++
		err := env.GetWorkflowError()
		if err == nil {
			var out json.RawMessage
			if gerr := env.GetWorkflowResult(&out); gerr != nil {
				t.Fatal(gerr)
			}
			res.PerOutput = append(res.PerOutput, out)
			res.PerErr = append(res.PerErr, nil)
			res.Output = out
			res.Err = nil
			return res
		}
		res.PerErr = append(res.PerErr, err)
		var wf *sdktemporal.WorkflowExecutionError
		if !errors.As(err, &wf) {
			if !opts.allowFlowError {
				t.Fatalf("execution %d: workflow failed: %v", res.Execs-1, err)
			}
			res.Err = err
			return res
		}
		var can *workflow.ContinueAsNewError
		if !errors.As(wf.Unwrap(), &can) {
			if !opts.allowFlowError {
				t.Fatalf("execution %d: workflow failed: %v", res.Execs-1, err)
			}
			res.Err = err
			return res
		}
		var next HostInput
		if derr := conv.FromPayloads(can.Input, &next); derr != nil {
			t.Fatal(derr)
		}
		cur = next
		res.Inputs = append(res.Inputs, cur)
	}
	t.Fatal("too many executions")
	return res
}

func chainRuntime(t *testing.T, threshold int) *Runtime {
	t.Helper()
	opts := testOptions(t)
	opts.CheckpointEventThreshold = threshold
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterStep(t, rt, "s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	return rt
}

// execRunLog holds one record per workflow execution; call begin at the start of
// the flow function so records[exec] belongs to that execution.
type execRunLog[T any] struct {
	records []T
}

func (l *execRunLog[T]) begin() *T {
	var zero T
	l.records = append(l.records, zero)
	return &l.records[len(l.records)-1]
}

func (l *execRunLog[T]) at(exec int) *T {
	if exec < 0 || exec >= len(l.records) {
		return nil
	}
	return &l.records[exec]
}

func isContinueAsNewWorkflowErr(err error) bool {
	return workflow.IsContinueAsNewError(err)
}
