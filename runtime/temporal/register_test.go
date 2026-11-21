package temporal

import (
	"encoding/json"
	"errors"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestNewRequiresOutbox(t *testing.T) {
	_, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err %v", err)
	}
	if pe.Code != core.CodeInvalidArgument || pe.Message != "temporal: outbox is required" {
		t.Fatalf("code=%s msg=%q", pe.Code, pe.Message)
	}
}

// activityNameSpyRegistry records Register*WithOptions names; unimplemented Registry methods panic via nil embed.
type activityNameSpyRegistry struct {
	worker.Registry
	names []string
}

var _ worker.Registry = (*activityNameSpyRegistry)(nil)

func (r *activityNameSpyRegistry) RegisterWorkflow(w any) {}

func (r *activityNameSpyRegistry) RegisterWorkflowWithOptions(w any, o workflow.RegisterOptions) {
	r.names = append(r.names, o.Name)
}

func (r *activityNameSpyRegistry) RegisterDynamicWorkflow(w any, o workflow.DynamicRegisterOptions) {}

func (r *activityNameSpyRegistry) RegisterActivity(a any) {}

func (r *activityNameSpyRegistry) RegisterActivityWithOptions(a any, o activity.RegisterOptions) {
	r.names = append(r.names, o.Name)
}

func (r *activityNameSpyRegistry) RegisterDynamicActivity(a any, o activity.DynamicRegisterOptions) {}

func TestRegisterRegistersEmitAndStepActivities(t *testing.T) {
	rt, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	reg := &activityNameSpyRegistry{}
	if err := rt.Register(reg); err != nil {
		t.Fatal(err)
	}
	want := []string{HostWorkflowName, stepActivityName, emitActivityName}
	if len(reg.names) != 3 {
		t.Fatalf("names %v", reg.names)
	}
	for i, n := range want {
		if reg.names[i] != n {
			t.Fatalf("[%d] %q want %q", i, reg.names[i], n)
		}
	}
}
