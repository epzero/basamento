package temporal

import (
	"context"
	"encoding/json"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// TestHostInputDecodesPartAWireName guards the recorded input shape: a history
// written with the untagged field names carries "RunID".
func TestHostInputDecodesPartAWireName(t *testing.T) {
	var in HostInput
	if err := json.Unmarshal([]byte(`{"Flow":{"Name":"f"},"Input":null,"RunID":"run-1"}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.RunID != "run-1" {
		t.Fatalf("RunID = %q want run-1", in.RunID)
	}
	var current HostInput
	if err := json.Unmarshal([]byte(`{"flow":{"Name":"f"},"input":null,"run_id":"run-2"}`), &current); err != nil {
		t.Fatal(err)
	}
	if current.RunID != "run-2" {
		t.Fatalf("RunID = %q want run-2", current.RunID)
	}
}

// TestReplayPartAHistory replays a history whose start input uses the untagged
// "RunID" field; the run id must still seed Rand.
func TestReplayPartAHistory(t *testing.T) {
	rt, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	partAInput := json.RawMessage(`{"Flow":{"Name":"flow-a","Hash":""},"Input":null,"RunID":"part-a-run"}`)
	aware := rt.DataConverter().(interface {
		WithContext(context.Context) converter.DataConverter
	})
	input, err := aware.WithContext(core.WithTenant(context.Background(), "acme")).ToPayloads(partAInput)
	if err != nil {
		t.Fatal(err)
	}
	tenantPayload, err := converter.GetDefaultDataConverter().ToPayload("acme")
	if err != nil {
		t.Fatal(err)
	}
	history := &historypb.History{Events: []*historypb.HistoryEvent{
		{
			EventId:   1,
			EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
				WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
					WorkflowType: &commonpb.WorkflowType{Name: HostWorkflowName},
					TaskQueue:    &taskqueuepb.TaskQueue{Name: rt.taskQueue},
					Input:        input,
					Header:       &commonpb.Header{Fields: map[string]*commonpb.Payload{tenantHeaderKey: tenantPayload}},
				},
			},
		},
		{
			EventId:   2,
			EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{
				WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{
					TaskQueue: &taskqueuepb.TaskQueue{Name: rt.taskQueue},
				},
			},
		},
		{EventId: 3, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED},
		{
			EventId:   4,
			EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{
				WorkflowTaskCompletedEventAttributes: &historypb.WorkflowTaskCompletedEventAttributes{
					ScheduledEventId: 2,
					StartedEventId:   3,
				},
			},
		},
		{
			EventId:   5,
			EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{
				WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{
					WorkflowTaskCompletedEventId: 4,
				},
			},
		},
	}}

	var got uint64
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		got = dc.Rand("seed").Uint64()
		return json.RawMessage(`null`), nil
	})
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
		DataConverter:            rt.DataConverter(),
		ContextPropagators:       []workflow.ContextPropagator{TenantPropagator{}},
		DisableDeadlockDetection: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayer.RegisterWorkflowWithOptions(rt.hostWorkflow, workflow.RegisterOptions{Name: HostWorkflowName})
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal(err)
	}
	want := seededRand("part-a-run", "seed").Uint64()
	if got != want {
		t.Fatalf("replayed Rand = %d want %d (run id lost)", got, want)
	}
}
