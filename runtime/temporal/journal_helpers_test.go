package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/runtime"
)

func mustNew(t *testing.T, opts Options) *Runtime {
	t.Helper()
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func mustRegisterStep(t *testing.T, rt *Runtime, name string, fn runtime.StepFunc, d runtime.StepDefaults) {
	t.Helper()
	if err := rt.RegisterStep(name, fn, d); err != nil {
		t.Fatal(err)
	}
}

func mustRegisterFlow(t *testing.T, rt *Runtime, h runtime.FlowHost) {
	t.Helper()
	if err := rt.RegisterFlowHost(h); err != nil {
		t.Fatal(err)
	}
}

func mustRegister(t *testing.T, rt *Runtime, env *testsuite.TestWorkflowEnvironment) {
	t.Helper()
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
}

func registerAcmeEnv(t *testing.T, rt *Runtime, env *testsuite.TestWorkflowEnvironment) {
	t.Helper()
	setTestTenantHeader(env, "acme")
	mustRegister(t, rt, env)
}

func mustUnmarshal(t *testing.T, data json.RawMessage, out any) {
	t.Helper()
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func assertDeepEqual(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("mismatch:\n  want: %+v\n   got: %+v", want, got)
	}
}

func registerCountingSteps(t *testing.T, rt *Runtime, names []string, counters map[string]*atomic.Int32) {
	t.Helper()
	reg := func(name string, ctr *atomic.Int32) func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return func(context.Context, json.RawMessage) (json.RawMessage, error) {
			ctr.Add(1)
			return json.RawMessage(fmt.Sprintf(`"%s"`, name)), nil
		}
	}
	for _, n := range names {
		c := &atomic.Int32{}
		counters[n] = c
		mustRegisterStep(t, rt, n, reg(n, c), runtime.StepDefaults{})
	}
}

type timerListenerStats struct {
	scheduled []time.Duration
	fired     int
}

func attachTimerListeners(env *testsuite.TestWorkflowEnvironment, obs *timerListenerStats) {
	env.SetOnTimerScheduledListener(func(_ string, d time.Duration) {
		obs.scheduled = append(obs.scheduled, d)
	})
	env.SetOnTimerFiredListener(func(_ string) {
		obs.fired++
	})
}

type idRandCapture struct {
	IDs   []string `json:"ids"`
	Rands []uint64 `json:"rands"`
}

func mustExecRunLogEntry[T any](t *testing.T, log *execRunLog[T], exec int) *T {
	t.Helper()
	p := log.at(exec)
	if p == nil {
		t.Fatalf("execution %d log missing", exec)
	}
	return p
}
