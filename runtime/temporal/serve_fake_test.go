package temporal

import (
	"context"
	"sync"

	"github.com/nexus-rpc/sdk-go/nexus"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
)

type fakeWorkflowService struct {
	workflowservice.WorkflowServiceClient
	parent        *fakeClient
	version       string
	err           error
	block         <-chan struct{}
	entered       chan struct{}
	onSuccessHook func(context.Context)
}

func (f *fakeWorkflowService) GetSystemInfo(ctx context.Context, _ *workflowservice.GetSystemInfoRequest, _ ...grpc.CallOption) (*workflowservice.GetSystemInfoResponse, error) {
	f.parent.recordServeOp("get_system_info")
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.onSuccessHook != nil {
		f.onSuccessHook(ctx)
	}
	return &workflowservice.GetSystemInfoResponse{ServerVersion: f.version}, nil
}

type fakeOperatorService struct {
	operatorservice.OperatorServiceClient
	parent            *fakeClient
	custom            map[string]enumspb.IndexedValueType
	listErr           error
	addErr            error
	addCalls          int
	lastList          *operatorservice.ListSearchAttributesRequest
	lastAdd           *operatorservice.AddSearchAttributesRequest
	onListSuccessHook func(context.Context)
	onAddSuccessHook  func(context.Context)
}

func (f *fakeOperatorService) ListSearchAttributes(ctx context.Context, req *operatorservice.ListSearchAttributesRequest, _ ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error) {
	f.parent.recordServeOp("list_search_attributes")
	f.parent.mu.Lock()
	f.lastList = req
	f.parent.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.onListSuccessHook != nil {
		f.onListSuccessHook(ctx)
	}
	return &operatorservice.ListSearchAttributesResponse{CustomAttributes: f.custom}, nil
}

func (f *fakeOperatorService) AddSearchAttributes(ctx context.Context, req *operatorservice.AddSearchAttributesRequest, _ ...grpc.CallOption) (*operatorservice.AddSearchAttributesResponse, error) {
	f.parent.recordServeOp("add_search_attributes")
	f.parent.mu.Lock()
	f.addCalls++
	f.lastAdd = req
	f.parent.mu.Unlock()
	if f.addErr != nil {
		return nil, f.addErr
	}
	if f.onAddSuccessHook != nil {
		f.onAddSuccessHook(ctx)
	}
	return &operatorservice.AddSearchAttributesResponse{}, nil
}

func (f *fakeClient) WorkflowService() workflowservice.WorkflowServiceClient {
	f.mu.Lock()
	if f.wfSvc == nil {
		f.wfSvc = &fakeWorkflowService{parent: f, version: "1.32.2"}
	}
	svc := f.wfSvc
	f.mu.Unlock()
	return svc
}

func (f *fakeClient) OperatorService() operatorservice.OperatorServiceClient {
	f.mu.Lock()
	if f.opSvc == nil {
		f.opSvc = &fakeOperatorService{parent: f}
	}
	svc := f.opSvc
	f.mu.Unlock()
	return svc
}

func (f *fakeClient) recordServeOp(op string) {
	f.mu.Lock()
	f.serveOps = append(f.serveOps, op)
	f.mu.Unlock()
}

func (f *fakeClient) ServeOps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.serveOps))
	copy(out, f.serveOps)
	return out
}

func (f *fakeClient) operatorAddCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.opSvc == nil {
		return 0
	}
	return f.opSvc.addCalls
}

func (f *fakeClient) lastListSearchAttributes() *operatorservice.ListSearchAttributesRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.opSvc == nil {
		return nil
	}
	return f.opSvc.lastList
}

func (f *fakeClient) lastAddSearchAttributes() *operatorservice.AddSearchAttributesRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.opSvc == nil {
		return nil
	}
	return f.opSvc.lastAdd
}

type fakeWorker struct {
	mu          sync.Mutex
	queue       string
	opts        worker.Options
	workflows   []string
	activities  []string
	startErr    error
	onStart     func()
	startCount  int
	stopCount   int
	started     chan struct{}
	onceStarted sync.Once
}

func (w *fakeWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.startCount++
	if w.onStart != nil {
		w.onStart()
	}
	if w.startErr != nil {
		return w.startErr
	}
	w.onceStarted.Do(func() {
		if w.started != nil {
			close(w.started)
		}
	})
	return nil
}

func (w *fakeWorker) Stop() {
	w.mu.Lock()
	w.stopCount++
	w.mu.Unlock()
}

func (w *fakeWorker) Run(<-chan interface{}) error {
	panic("fakeWorker.Run")
}

func (w *fakeWorker) RegisterWorkflow(interface{}) {
	panic("unexpected RegisterWorkflow")
}

func (w *fakeWorker) RegisterWorkflowWithOptions(_ interface{}, opts workflow.RegisterOptions) {
	w.mu.Lock()
	w.workflows = append(w.workflows, opts.Name)
	w.mu.Unlock()
}

func (w *fakeWorker) RegisterDynamicWorkflow(interface{}, workflow.DynamicRegisterOptions) {
	panic("unexpected RegisterDynamicWorkflow")
}

func (w *fakeWorker) RegisterActivity(interface{}) {
	panic("unexpected RegisterActivity")
}

func (w *fakeWorker) RegisterActivityWithOptions(_ interface{}, opts activity.RegisterOptions) {
	w.mu.Lock()
	w.activities = append(w.activities, opts.Name)
	w.mu.Unlock()
}

func (w *fakeWorker) RegisterDynamicActivity(interface{}, activity.DynamicRegisterOptions) {
	panic("unexpected RegisterDynamicActivity")
}

func (w *fakeWorker) RegisterNexusService(*nexus.Service) {
	panic("unexpected RegisterNexusService")
}

func (w *fakeWorker) snapshot() (queue string, opts worker.Options, wfs, acts []string, starts, stops int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.queue, w.opts, append([]string(nil), w.workflows...), append([]string(nil), w.activities...), w.startCount, w.stopCount
}
