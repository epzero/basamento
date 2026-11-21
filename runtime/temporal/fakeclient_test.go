package temporal

import (
	"context"
	"sync"
	"testing"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

type fakeWorkflowRun struct {
	id    string
	runID string
}

func (f fakeWorkflowRun) GetID() string    { return f.id }
func (f fakeWorkflowRun) GetRunID() string { return f.runID }
func (f fakeWorkflowRun) Get(ctx context.Context, valuePtr interface{}) error {
	return nil
}
func (f fakeWorkflowRun) GetWithOptions(ctx context.Context, valuePtr interface{}, options client.WorkflowRunGetOptions) error {
	return nil
}

type fakeUpdateHandle struct {
	getErr error
}

func (f fakeUpdateHandle) Get(ctx context.Context, valuePtr interface{}) error {
	return f.getErr
}
func (f fakeUpdateHandle) UpdateID() string   { return "upd" }
func (f fakeUpdateHandle) RunID() string      { return "" }
func (f fakeUpdateHandle) WorkflowID() string { return "" }

type fakeClientCall struct {
	Op string
	// fields used per op
	ExecuteOpts client.StartWorkflowOptions
	ExecuteName string
	ExecuteArgs []any
	CancelID    string
	CancelRunID string
	DescribeID  string
	ListReq     *workflowservice.ListWorkflowExecutionsRequest
	UpdateOpts  client.UpdateWorkflowOptions
	DialOpts    client.Options
}

type fakeClient struct {
	client.Client
	mu    sync.Mutex
	calls []fakeClientCall

	executeErr   error
	executeRun   fakeWorkflowRun
	cancelErr    error
	describeResp *workflowservice.DescribeWorkflowExecutionResponse
	describeErr  error
	listResp     *workflowservice.ListWorkflowExecutionsResponse
	listErr      error
	updateHandle client.WorkflowUpdateHandle
	updateErr    error
	closeCount   int
}

func (f *fakeClient) Calls() []fakeClientCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeClientCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeClient) CloseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCount
}

func (f *fakeClient) lastCall(op string) (fakeClientCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Op == op {
			return f.calls[i], true
		}
	}
	return fakeClientCall{}, false
}

func (f *fakeClient) dialCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Op == "dial" {
			n++
		}
	}
	return n
}

func (f *fakeClient) clientOps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		switch c.Op {
		case "execute", "cancel", "describe", "list", "update":
			n++
		}
	}
	return n
}

func (f *fakeClient) recordDial(opts client.Options) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeClientCall{Op: "dial", DialOpts: opts})
	f.mu.Unlock()
}

func mustLastCall(fc *fakeClient, op string) fakeClientCall {
	c, ok := fc.lastCall(op)
	if !ok {
		panic("no " + op + " call")
	}
	return c
}

func (f *fakeClient) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeClientCall{
		Op: "execute", ExecuteOpts: options, ExecuteName: workflow.(string),
		ExecuteArgs: args,
	})
	f.mu.Unlock()
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	run := f.executeRun
	if run.id == "" {
		run.id = options.ID
	}
	return run, nil
}

func (f *fakeClient) CancelWorkflow(ctx context.Context, workflowID string, runID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, fakeClientCall{Op: "cancel", CancelID: workflowID, CancelRunID: runID})
	f.mu.Unlock()
	return f.cancelErr
}

func (f *fakeClient) DescribeWorkflowExecution(ctx context.Context, workflowID string, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeClientCall{Op: "describe", DescribeID: workflowID})
	f.mu.Unlock()
	return f.describeResp, f.describeErr
}

func (f *fakeClient) ListWorkflow(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeClientCall{Op: "list", ListReq: request})
	f.mu.Unlock()
	return f.listResp, f.listErr
}

func (f *fakeClient) UpdateWorkflow(ctx context.Context, options client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeClientCall{Op: "update", UpdateOpts: options})
	f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.updateHandle, nil
}

func (f *fakeClient) Close() {
	f.mu.Lock()
	f.closeCount++
	f.mu.Unlock()
}

func testControlRuntime(t *testing.T, fc *fakeClient) *Runtime {
	t.Helper()
	rt, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(opts client.Options) (client.Client, error) {
		fc.recordDial(opts)
		return fc, nil
	}
	rt.newRunID = func() (string, error) { return "run_fixed", nil }
	return rt
}
