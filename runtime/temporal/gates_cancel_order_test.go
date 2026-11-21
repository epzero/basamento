package temporal

import (
	"encoding/json"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/runtime"
)

const gateCancelOrderWorkflow = "gate-cancel-order-probe"

type orderSettable struct {
	workflow.Settable
	name  string
	order *[]string
}

func (s orderSettable) Set(v any, err error) {
	*s.order = append(*s.order, s.name)
	s.Settable.Set(v, err)
}

func (s orderSettable) SetError(err error) {
	*s.order = append(*s.order, s.name)
	s.Settable.SetError(err)
}

func registerGateCancelOrderProbe(env *testsuite.TestWorkflowEnvironment, rt *Runtime) {
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ struct{}) ([]string, error) {
		dc := newDurableContext(ctx, rt, HostInput{RunID: "r1"}, tenantFromWorkflow(ctx))
		if err := dc.registerGateUpdateHandler(); err != nil {
			return nil, err
		}
		f1 := dc.Await(runtime.GateSpec{Name: "g1"})
		f2 := dc.Await(runtime.GateSpec{Name: "g2"})
		var order []string
		dc.gates["g1"].set = orderSettable{Settable: dc.gates["g1"].set, name: "g1", order: &order}
		dc.gates["g2"].set = orderSettable{Settable: dc.gates["g2"].set, name: "g2", order: &order}
		_ = f1.Get(nil)
		_ = f2.Get(nil)
		return order, nil
	}, workflow.RegisterOptions{Name: gateCancelOrderWorkflow})
}

func runGateCancelOrderProbe(t *testing.T) []string {
	t.Helper()
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	registerGateCancelOrderProbe(env, rt)
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Second)
	env.ExecuteWorkflow(gateCancelOrderWorkflow, struct{}{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var order []string
	if err := env.GetWorkflowResult(&order); err != nil {
		t.Fatal(err)
	}
	return order
}

// TestGateCancellationSettlesInNameOrderOnReplay runs the same cancellation
// several times: map iteration would pick a different branch on some runs.
func TestGateCancellationSettlesInNameOrderOnReplay(t *testing.T) {
	for i := 0; i < 5; i++ {
		order := runGateCancelOrderProbe(t)
		if len(order) != 2 || order[0] != "g1" || order[1] != "g2" {
			t.Fatalf("run %d settled %v want [g1 g2]", i, order)
		}
	}
}
