package temporal

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestContinuationSelectReplayThenLive(t *testing.T) {
	rt := chainRuntime(t, 10)
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		fast := dc.Sleep(time.Minute)
		slow := dc.Sleep(time.Hour)
		first, err := dc.Select(slow, fast)
		if err != nil {
			return nil, err
		}
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		a := dc.Sleep(time.Minute)
		b := dc.Sleep(time.Hour)
		second, err := dc.Select(b, a)
		if err != nil {
			return nil, err
		}
		c := dc.Sleep(time.Hour)
		d := dc.Sleep(time.Minute)
		third, err := dc.Select(d, c)
		if err != nil {
			return nil, err
		}
		return json.Marshal([]int{first, second, third})
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "j"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	if string(res.Output) != `[1,1,0]` {
		t.Fatalf("select %s", res.Output)
	}
}

func TestContinuationSelectReplayMismatchIsInternalError(t *testing.T) {
	rt := chainRuntime(t, 10)
	var execIdx int
	opts := defaultChainOpts()
	opts.allowFlowError = true
	opts.perExec = []func(int, *testsuite.TestWorkflowEnvironment){
		func(exec int, _ *testsuite.TestWorkflowEnvironment) { execIdx = exec },
		func(exec int, _ *testsuite.TestWorkflowEnvironment) { execIdx = exec },
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		useOne := execIdx >= 1
		fast := dc.Sleep(time.Minute)
		slow := dc.Sleep(time.Hour)
		if useOne {
			_, err := dc.Select(fast)
			return nil, err
		}
		if _, err := dc.Select(slow, fast); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		return json.RawMessage(`"ok"`), nil
	})
	res := runChain(t, rt, HostInput{
		Flow:  runtime.FlowRef{Name: "j"},
		RunID: "r1",
		Input: json.RawMessage(`{}`),
	}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs=%d", res.Execs)
	}
	err := res.PerErr[1]
	var pe *core.Error
	var wf *sdktemporal.WorkflowExecutionError
	if errors.As(err, &wf) {
		var ae *sdktemporal.ApplicationError
		if errors.As(wf.Unwrap(), &ae) {
			pe = &core.Error{Code: core.Code(ae.Type()), Message: ae.Message()}
		}
	}
	if pe == nil || pe.Code != core.CodeInternal || pe.Message != "temporal: replayed select does not match the flow" {
		t.Fatalf("err %+v", err)
	}
}
