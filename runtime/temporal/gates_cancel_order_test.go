package temporal

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/runtime"
)

const (
	gateCancelOrderWorkflow = "gate-cancel-order-probe"
	gateCancelOrderTimeout  = 10 * time.Second
)

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
		var order []string
		// The timeouts arm a timer per gate; cancellation cancels them, and the
		// cancel-call order is the CancelTimer command order this test protects.
		f1 := dc.Await(runtime.GateSpec{Name: "g1", Timeout: gateCancelOrderTimeout})
		f2 := dc.Await(runtime.GateSpec{Name: "g2", Timeout: gateCancelOrderTimeout})
		for _, name := range []string{"g1", "g2"} {
			name := name
			g := dc.gates[name]
			if g.timerCancel == nil {
				return nil, fmt.Errorf("gate %s has no timeout timer", name)
			}
			cancel := g.timerCancel
			g.timerCancel = func() {
				order = append(order, "cancel:"+name)
				cancel()
			}
		}
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
// several times: map iteration would pick a different branch on some runs. It
// also checks that the timeout timers are cancelled in the same order, so the
// CancelTimer commands do not depend on map iteration.
func TestGateCancellationSettlesInNameOrderOnReplay(t *testing.T) {
	want := []string{"cancel:g1", "g1", "cancel:g2", "g2"}
	for i := 0; i < 5; i++ {
		order := runGateCancelOrderProbe(t)
		if len(order) != len(want) {
			t.Fatalf("run %d order %v want %v", i, order, want)
		}
		for j := range want {
			if order[j] != want[j] {
				t.Fatalf("run %d order %v want %v", i, order, want)
			}
		}
	}
}
