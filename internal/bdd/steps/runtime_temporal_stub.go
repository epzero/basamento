package steps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	namespacepb "go.temporal.io/api/namespace/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	updatepb "go.temporal.io/api/update/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// temporalStub is an in-process Temporal frontend for the BDD harness. It runs
// no workflow code; ListWorkflowExecutions orders by start time descending with
// later insertion winning ties (differs from the server's SQL visibility ordering).
type temporalStub struct {
	workflowservice.UnimplementedWorkflowServiceServer
	operatorservice.UnimplementedOperatorServiceServer

	mu      sync.Mutex
	now     func() time.Time
	version string

	grpcServer  *grpc.Server
	bufListener *bufconn.Listener
	hostPort    string
	closeOnce   sync.Once
	closeCh     chan struct{}

	pollSeen map[string]bool
	pollWake chan struct{}

	runSeq uint64

	records      []*stubExecutionRecord
	starts       []stubStart
	latestByWfID map[string]*stubExecutionRecord

	customSearchAttrs map[string]enumspb.IndexedValueType
	requests          []string
	updates           []stubUpdateRecord
}

type stubStart struct {
	WorkflowID       string
	RunID            string
	WorkflowType     string
	TaskQueue        string
	SearchAttributes map[string]string
	StartTime        time.Time
}

type stubExecution struct {
	WorkflowID      string
	RunID           string
	Status          enumspb.WorkflowExecutionStatus
	CloseTime       *time.Time
	CancelRequested bool
}

type stubUpdateRecord struct {
	Name       string
	WorkflowID string
}

type stubExecutionRecord struct {
	stubStart
	status          enumspb.WorkflowExecutionStatus
	closeTime       *time.Time
	cancelRequested bool
	recordIndex     int
}

const stubNamespace = "default"

var (
	systemSearchAttributes = map[string]enumspb.IndexedValueType{
		"WorkflowId":      enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		"ExecutionStatus": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		"StartTime":       enumspb.INDEXED_VALUE_TYPE_DATETIME,
		"CloseTime":       enumspb.INDEXED_VALUE_TYPE_DATETIME,
	}

	failedCloseStatuses = map[enumspb.WorkflowExecutionStatus]struct{}{
		enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:     {},
		enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:   {},
		enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED: {},
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:  {},
	}
)

func stubUnaryServiceError(
	ctx context.Context,
	req any,
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	resp, err := handler(ctx, req)
	if err != nil {
		var se serviceerror.ServiceError
		if errors.As(err, &se) {
			return resp, serviceerror.ToStatus(err).Err()
		}
	}
	return resp, err
}

func newTemporalStub(now func() time.Time, version string) (*temporalStub, error) {
	if now == nil {
		return nil, fmt.Errorf("temporalStub: now function required")
	}
	s := &temporalStub{
		now:               now,
		version:           version,
		closeCh:           make(chan struct{}),
		pollSeen:          map[string]bool{},
		pollWake:          make(chan struct{}),
		latestByWfID:      map[string]*stubExecutionRecord{},
		customSearchAttrs: map[string]enumspb.IndexedValueType{},
		hostPort:          "passthrough:///stub",
		bufListener:       bufconn.Listen(1 << 20),
	}
	s.grpcServer = grpc.NewServer(grpc.UnaryInterceptor(stubUnaryServiceError))
	workflowservice.RegisterWorkflowServiceServer(s.grpcServer, s)
	operatorservice.RegisterOperatorServiceServer(s.grpcServer, s)
	go func() {
		_ = s.grpcServer.Serve(s.bufListener)
	}()
	return s, nil
}

// ClientOptions returns SDK client options that dial this stub in-process.
func (s *temporalStub) ClientOptions() client.Options {
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return s.bufListener.DialContext(ctx)
	}
	return client.Options{
		HostPort:  s.hostPort,
		Namespace: stubNamespace,
		ConnectionOptions: client.ConnectionOptions{
			DialOptions: []grpc.DialOption{
				grpc.WithContextDialer(dialer),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			},
		},
	}
}

// SetVersion changes the ServerVersion returned by GetSystemInfo.
func (s *temporalStub) SetVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = v
}

// Close stops the gRPC server, unblocks pending polls and waiters; idempotent.
func (s *temporalStub) Close() {
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.grpcServer.GracefulStop()
	})
}

// PreRegisterSearchAttribute registers a custom search attribute known to the stub namespace.
func (s *temporalStub) PreRegisterSearchAttribute(name string, t enumspb.IndexedValueType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customSearchAttrs[name] = t
}

// SearchAttributes returns a copy of custom search attributes registered so far.
func (s *temporalStub) SearchAttributes() map[string]enumspb.IndexedValueType {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]enumspb.IndexedValueType, len(s.customSearchAttrs))
	for k, v := range s.customSearchAttrs {
		out[k] = v
	}
	return out
}

// Requests returns a copy of received RPC names as Service/Method in arrival order.
func (s *temporalStub) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Starts returns a copy of every accepted workflow start recorded by the stub.
func (s *temporalStub) Starts() []stubStart {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stubStart, len(s.starts))
	for i, st := range s.starts {
		out[i] = copyStubStart(st)
	}
	return out
}

// Executions returns a copy of execution records and their statuses.
func (s *temporalStub) Executions() []stubExecution {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stubExecution, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, stubExecution{
			WorkflowID:      rec.WorkflowID,
			RunID:           rec.RunID,
			Status:          rec.status,
			CloseTime:       cloneTime(rec.closeTime),
			CancelRequested: rec.cancelRequested,
		})
	}
	return out
}

// Updates returns a copy of workflow update requests recorded by the stub.
func (s *temporalStub) Updates() []stubUpdateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubUpdateRecord(nil), s.updates...)
}

// Finish closes a running execution with the given terminal status at the injected clock time.
func (s *temporalStub) Finish(workflowID string, status enumspb.WorkflowExecutionStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.latestByWfID[workflowID]
	if rec == nil || !isRunningStatus(rec.status) {
		return serviceerror.NewNotFound("workflow not found")
	}
	now := s.now()
	rec.status = status
	rec.closeTime = &now
	return nil
}

// WaitForPoll blocks until at least one task poll arrives, ctx ends, or Close is called.
func (s *temporalStub) WaitForPoll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for {
		s.mu.Lock()
		if len(s.pollSeen) > 0 {
			s.mu.Unlock()
			return nil
		}
		wake := s.pollWake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closeCh:
			return context.Canceled
		}
	}
}

// WaitForPolls blocks until a poll of every listed kind has been seen, ctx ends, or Close is called.
func (s *temporalStub) WaitForPolls(ctx context.Context, kinds ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, k := range kinds {
		if k != "workflow" && k != "activity" {
			return serviceerror.NewInvalidArgument(fmt.Sprintf("stub: unknown poll kind %q", k))
		}
	}
	for {
		s.mu.Lock()
		all := true
		for _, k := range kinds {
			if !s.pollSeen[k] {
				all = false
				break
			}
		}
		if all {
			s.mu.Unlock()
			return nil
		}
		wake := s.pollWake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closeCh:
			return context.Canceled
		}
	}
}

func (s *temporalStub) recordRequest(method string) {
	s.requests = append(s.requests, method)
}

func (s *temporalStub) GetSystemInfo(
	_ context.Context,
	_ *workflowservice.GetSystemInfoRequest,
) (*workflowservice.GetSystemInfoResponse, error) {
	s.mu.Lock()
	s.recordRequest("WorkflowService/GetSystemInfo")
	ver := s.version
	s.mu.Unlock()
	return &workflowservice.GetSystemInfoResponse{
		ServerVersion: ver,
		Capabilities:  &workflowservice.GetSystemInfoResponse_Capabilities{},
	}, nil
}

func (s *temporalStub) DescribeNamespace(
	_ context.Context,
	req *workflowservice.DescribeNamespaceRequest,
) (*workflowservice.DescribeNamespaceResponse, error) {
	s.mu.Lock()
	s.recordRequest("WorkflowService/DescribeNamespace")
	s.mu.Unlock()
	if req.GetNamespace() != "" && req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	return &workflowservice.DescribeNamespaceResponse{
		NamespaceInfo: &namespacepb.NamespaceInfo{
			Name:  stubNamespace,
			State: enumspb.NAMESPACE_STATE_REGISTERED,
		},
	}, nil
}

func (s *temporalStub) ShutdownWorker(
	_ context.Context,
	_ *workflowservice.ShutdownWorkerRequest,
) (*workflowservice.ShutdownWorkerResponse, error) {
	s.mu.Lock()
	s.recordRequest("WorkflowService/ShutdownWorker")
	s.mu.Unlock()
	return &workflowservice.ShutdownWorkerResponse{}, nil
}

func (s *temporalStub) StartWorkflowExecution(
	_ context.Context,
	req *workflowservice.StartWorkflowExecutionRequest,
) (*workflowservice.StartWorkflowExecutionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequest("WorkflowService/StartWorkflowExecution")

	if req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	if strings.TrimSpace(req.GetWorkflowId()) == "" {
		return nil, serviceerror.NewInvalidArgument("WorkflowId is not set.")
	}
	if req.GetWorkflowType() == nil || req.GetWorkflowType().GetName() == "" {
		return nil, serviceerror.NewInvalidArgument("WorkflowType is not set.")
	}
	if strings.TrimSpace(req.GetTaskQueue().GetName()) == "" {
		return nil, serviceerror.NewInvalidArgument("TaskQueue is not set.")
	}

	reuse := req.GetWorkflowIdReusePolicy()
	conflict := req.GetWorkflowIdConflictPolicy()
	if err := normalizeAndValidateIDPolicies(&reuse, &conflict); err != nil {
		return nil, err
	}

	saStrings, err := s.decodeSearchAttributes(req.GetSearchAttributes())
	if err != nil {
		return nil, err
	}

	current := s.latestByWfID[req.GetWorkflowId()]
	if current != nil {
		if isRunningStatus(current.status) {
			switch conflict {
			case enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL:
				return nil, serviceerror.NewWorkflowExecutionAlreadyStarted(
					fmt.Sprintf("Workflow execution is already running. WorkflowId: %v, RunId: %v.", req.GetWorkflowId(), current.RunID),
					"", current.RunID,
				)
			case enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING:
				return &workflowservice.StartWorkflowExecutionResponse{
					RunId:   current.RunID,
					Started: false,
				}, nil
			default:
				return nil, serviceerror.NewInvalidArgument("stub: not supported")
			}
		}
		if err := checkReusePolicy(current, reuse); err != nil {
			return nil, err
		}
	}

	runID := s.nextRunID()
	start := s.now()
	st := stubStart{
		WorkflowID:       req.GetWorkflowId(),
		RunID:            runID,
		WorkflowType:     req.GetWorkflowType().GetName(),
		TaskQueue:        req.GetTaskQueue().GetName(),
		SearchAttributes: saStrings,
		StartTime:        start,
	}
	rec := &stubExecutionRecord{
		stubStart:   st,
		status:      enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		recordIndex: len(s.records),
	}
	s.records = append(s.records, rec)
	s.starts = append(s.starts, copyStubStart(st))
	s.latestByWfID[req.GetWorkflowId()] = rec

	return &workflowservice.StartWorkflowExecutionResponse{
		RunId:               runID,
		FirstExecutionRunId: runID,
		Started:             true,
	}, nil
}

func (s *temporalStub) DescribeWorkflowExecution(
	_ context.Context,
	req *workflowservice.DescribeWorkflowExecutionRequest,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequest("WorkflowService/DescribeWorkflowExecution")
	if req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	rec, err := s.findExecution(req.GetExecution())
	if err != nil {
		return nil, err
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: s.executionInfo(rec),
	}, nil
}

func (s *temporalStub) RequestCancelWorkflowExecution(
	_ context.Context,
	req *workflowservice.RequestCancelWorkflowExecutionRequest,
) (*workflowservice.RequestCancelWorkflowExecutionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequest("WorkflowService/RequestCancelWorkflowExecution")
	if req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	rec, err := s.findExecution(req.GetWorkflowExecution())
	if err != nil {
		return nil, err
	}
	if !isRunningStatus(rec.status) {
		return nil, serviceerror.NewNotFound("workflow execution already completed")
	}
	rec.cancelRequested = true
	return &workflowservice.RequestCancelWorkflowExecutionResponse{}, nil
}

func (s *temporalStub) UpdateWorkflowExecution(
	_ context.Context,
	req *workflowservice.UpdateWorkflowExecutionRequest,
) (*workflowservice.UpdateWorkflowExecutionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequest("WorkflowService/UpdateWorkflowExecution")
	if req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	rec, err := s.findExecution(req.GetWorkflowExecution())
	if err != nil {
		return nil, err
	}
	if !isRunningStatus(rec.status) {
		return nil, serviceerror.NewNotFound("workflow execution already completed")
	}
	name := req.GetRequest().GetInput().GetName()
	s.updates = append(s.updates, stubUpdateRecord{Name: name, WorkflowID: rec.WorkflowID})
	return &workflowservice.UpdateWorkflowExecutionResponse{
		Outcome: &updatepb.Outcome{
			Value: &updatepb.Outcome_Success{
				Success: &commonpb.Payloads{},
			},
		},
		Stage: enumspb.UPDATE_WORKFLOW_EXECUTION_LIFECYCLE_STAGE_COMPLETED,
	}, nil
}

func (s *temporalStub) ListWorkflowExecutions(
	_ context.Context,
	req *workflowservice.ListWorkflowExecutionsRequest,
) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequest("WorkflowService/ListWorkflowExecutions")
	if req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	basamentoRegistered := s.customSearchAttrs["BasamentoFlow"] != enumspb.INDEXED_VALUE_TYPE_UNSPECIFIED
	filter, err := parseTemporalListQuery(req.GetQuery(), basamentoRegistered)
	if err != nil {
		return nil, err
	}
	matched := s.filterExecutions(filter)
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].StartTime.Equal(matched[j].StartTime) {
			return matched[i].StartTime.After(matched[j].StartTime)
		}
		return matched[i].recordIndex > matched[j].recordIndex
	})

	pageSize := int(req.GetPageSize())
	if pageSize < 0 {
		return nil, serviceerror.NewInvalidArgument("invalid page size")
	}
	if pageSize == 0 {
		pageSize = 1000
	}
	offset := 0
	if len(req.GetNextPageToken()) > 0 {
		offset, err = decodePageToken(req.GetQuery(), req.GetNextPageToken())
		if err != nil {
			return nil, err
		}
	}
	if offset < 0 || offset > len(matched) {
		return nil, serviceerror.NewInvalidArgument("invalid page token offset")
	}
	end := offset + pageSize
	if end > len(matched) {
		end = len(matched)
	}
	infos := make([]*workflowpb.WorkflowExecutionInfo, 0, end-offset)
	for _, rec := range matched[offset:end] {
		infos = append(infos, s.executionInfo(rec))
	}
	var next []byte
	if end < len(matched) {
		next, err = encodePageToken(req.GetQuery(), end)
		if err != nil {
			return nil, err
		}
	}
	return &workflowservice.ListWorkflowExecutionsResponse{
		Executions:    infos,
		NextPageToken: next,
	}, nil
}

func (s *temporalStub) PollWorkflowTaskQueue(
	ctx context.Context,
	req *workflowservice.PollWorkflowTaskQueueRequest,
) (*workflowservice.PollWorkflowTaskQueueResponse, error) {
	s.mu.Lock()
	s.recordRequest("WorkflowService/PollWorkflowTaskQueue")
	s.markPollSeen("workflow")
	s.mu.Unlock()
	select {
	case <-s.closeCh:
		return &workflowservice.PollWorkflowTaskQueueResponse{}, nil
	case <-ctx.Done():
		return &workflowservice.PollWorkflowTaskQueueResponse{}, nil
	}
}

func (s *temporalStub) PollActivityTaskQueue(
	ctx context.Context,
	req *workflowservice.PollActivityTaskQueueRequest,
) (*workflowservice.PollActivityTaskQueueResponse, error) {
	s.mu.Lock()
	s.recordRequest("WorkflowService/PollActivityTaskQueue")
	s.markPollSeen("activity")
	s.mu.Unlock()
	select {
	case <-s.closeCh:
		return &workflowservice.PollActivityTaskQueueResponse{}, nil
	case <-ctx.Done():
		return &workflowservice.PollActivityTaskQueueResponse{}, nil
	}
}

func (s *temporalStub) ListSearchAttributes(
	_ context.Context,
	req *operatorservice.ListSearchAttributesRequest,
) (*operatorservice.ListSearchAttributesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequest("OperatorService/ListSearchAttributes")
	if req.GetNamespace() != "" && req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	return &operatorservice.ListSearchAttributesResponse{
		SystemAttributes: mapsCopyEnum(systemSearchAttributes),
		CustomAttributes: mapsCopyEnum(s.customSearchAttrs),
	}, nil
}

func (s *temporalStub) AddSearchAttributes(
	_ context.Context,
	req *operatorservice.AddSearchAttributesRequest,
) (*operatorservice.AddSearchAttributesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequest("OperatorService/AddSearchAttributes")
	if req.GetNamespace() != "" && req.GetNamespace() != stubNamespace {
		return nil, serviceerror.NewNamespaceNotFound(req.GetNamespace())
	}
	attrs := req.GetSearchAttributes()
	names := slices.Sorted(maps.Keys(attrs))
	pending := make(map[string]enumspb.IndexedValueType, len(names))
	for _, name := range names {
		typ := attrs[name]
		if typ == enumspb.INDEXED_VALUE_TYPE_UNSPECIFIED {
			return nil, serviceerror.NewInvalidArgument("Search attribute type must be specified")
		}
		if _, inSystem := systemSearchAttributes[name]; inSystem {
			return nil, serviceerror.NewAlreadyExists(fmt.Sprintf("Search attribute %s already exists.", name))
		}
		if _, ok := s.customSearchAttrs[name]; ok {
			return nil, serviceerror.NewAlreadyExists(fmt.Sprintf("Search attribute %s already exists.", name))
		}
		pending[name] = typ
	}
	for _, name := range names {
		s.customSearchAttrs[name] = pending[name]
	}
	return &operatorservice.AddSearchAttributesResponse{}, nil
}

func (s *temporalStub) markPollSeen(kind string) {
	s.pollSeen[kind] = true
	close(s.pollWake)
	s.pollWake = make(chan struct{})
}

func (s *temporalStub) nextRunID() string {
	s.runSeq++
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", s.runSeq)
}

func (s *temporalStub) findExecution(ex *commonpb.WorkflowExecution) (*stubExecutionRecord, error) {
	if ex == nil || strings.TrimSpace(ex.GetWorkflowId()) == "" {
		return nil, serviceerror.NewInvalidArgument("execution not set")
	}
	if strings.TrimSpace(ex.GetRunId()) != "" {
		for i := len(s.records) - 1; i >= 0; i-- {
			rec := s.records[i]
			if rec.WorkflowID == ex.GetWorkflowId() && rec.RunID == ex.GetRunId() {
				return rec, nil
			}
		}
		return nil, serviceerror.NewNotFound("workflow not found")
	}
	rec := s.latestByWfID[ex.GetWorkflowId()]
	if rec == nil {
		return nil, serviceerror.NewNotFound("workflow not found")
	}
	return rec, nil
}

func (s *temporalStub) executionInfo(rec *stubExecutionRecord) *workflowpb.WorkflowExecutionInfo {
	info := &workflowpb.WorkflowExecutionInfo{
		Execution: &commonpb.WorkflowExecution{
			WorkflowId: rec.WorkflowID,
			RunId:      rec.RunID,
		},
		Type:             &commonpb.WorkflowType{Name: rec.WorkflowType},
		TaskQueue:        rec.TaskQueue,
		Status:           rec.status,
		StartTime:        timestamppb.New(rec.StartTime),
		SearchAttributes: encodeSearchAttributesMap(rec.SearchAttributes),
		HistoryLength:    1,
	}
	if rec.closeTime != nil {
		info.CloseTime = timestamppb.New(*rec.closeTime)
	}
	return info
}

func (s *temporalStub) filterExecutions(f *listQueryFilter) []*stubExecutionRecord {
	var out []*stubExecutionRecord
	for _, rec := range s.records {
		if !strings.HasPrefix(rec.WorkflowID, f.workflowIDPrefix) {
			continue
		}
		if f.statusNot != nil && rec.status == f.statusNot.executionStatus() {
			continue
		}
		if f.statusEq != nil && rec.status != f.statusEq.executionStatus() {
			continue
		}
		if len(f.statusIN) > 0 {
			ok := false
			for _, st := range f.statusIN {
				if rec.status == st.executionStatus() {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if f.basamentoFlow != nil {
			if rec.SearchAttributes["BasamentoFlow"] != *f.basamentoFlow {
				continue
			}
		}
		out = append(out, rec)
	}
	return out
}

func (s *temporalStub) decodeSearchAttributes(sa *commonpb.SearchAttributes) (map[string]string, error) {
	if sa == nil || len(sa.GetIndexedFields()) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for name, payload := range sa.GetIndexedFields() {
		if isSystemSearchAttribute(name) {
			continue
		}
		if _, ok := s.customSearchAttrs[name]; !ok {
			return nil, serviceerror.NewInvalidArgument(
				fmt.Sprintf("unable to add search attributes: field '%s' not found in metadata", name),
			)
		}
		val, err := decodeKeywordPayload(payload)
		if err != nil {
			return nil, err
		}
		out[name] = val
	}
	return out, nil
}

func normalizeAndValidateIDPolicies(reuse *enumspb.WorkflowIdReusePolicy, conflict *enumspb.WorkflowIdConflictPolicy) error {
	if *reuse == enumspb.WORKFLOW_ID_REUSE_POLICY_TERMINATE_IF_RUNNING { //nolint:staticcheck // stub refuses deprecated policy paths the runtime must not use
		return serviceerror.NewInvalidArgument("stub: not supported")
	}
	if *conflict == enumspb.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING {
		return serviceerror.NewInvalidArgument("stub: not supported")
	}
	if *conflict == enumspb.WORKFLOW_ID_CONFLICT_POLICY_UNSPECIFIED {
		*conflict = enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL
	}
	if *reuse == enumspb.WORKFLOW_ID_REUSE_POLICY_UNSPECIFIED {
		*reuse = enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE
	}
	return nil
}

func checkReusePolicy(current *stubExecutionRecord, reuse enumspb.WorkflowIdReusePolicy) error {
	switch reuse {
	case enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE:
		return nil
	case enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE:
		return serviceerror.NewWorkflowExecutionAlreadyStarted(
			fmt.Sprintf("Workflow execution already finished. WorkflowId: %v, RunId: %v. Workflow Id reuse policy: reject duplicate workflow Id.", current.WorkflowID, current.RunID),
			"", current.RunID,
		)
	case enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY:
		if _, failed := failedCloseStatuses[current.status]; !failed {
			return serviceerror.NewWorkflowExecutionAlreadyStarted(
				fmt.Sprintf("Workflow execution already finished successfully. WorkflowId: %v, RunId: %v. Workflow Id reuse policy: allow duplicate workflow Id if last run failed.", current.WorkflowID, current.RunID),
				"", current.RunID,
			)
		}
		return nil
	default:
		return serviceerror.NewInvalidArgument("stub: not supported")
	}
}

func isRunningStatus(st enumspb.WorkflowExecutionStatus) bool {
	return st == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
}

func isSystemSearchAttribute(name string) bool {
	_, ok := systemSearchAttributes[name]
	return ok
}

func decodeKeywordPayload(p *commonpb.Payload) (string, error) {
	var val string
	if err := json.Unmarshal(p.GetData(), &val); err != nil {
		return "", serviceerror.NewInvalidArgument(fmt.Sprintf("invalid search attribute payload: %v", err))
	}
	return val, nil
}

func encodeSearchAttributesMap(vals map[string]string) *commonpb.SearchAttributes {
	if len(vals) == 0 {
		return nil
	}
	fields := map[string]*commonpb.Payload{}
	for k, v := range vals {
		data, _ := json.Marshal(v)
		fields[k] = &commonpb.Payload{
			Metadata: map[string][]byte{"encoding": []byte("json/plain")},
			Data:     data,
		}
	}
	return &commonpb.SearchAttributes{IndexedFields: fields}
}

type pageToken struct {
	Query  string `json:"q"`
	Offset int    `json:"o"`
}

func encodePageToken(query string, offset int) ([]byte, error) {
	b, err := json.Marshal(pageToken{Query: query, Offset: offset})
	if err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(b)), nil
}

func decodePageToken(query string, token []byte) (int, error) {
	raw, err := base64.StdEncoding.DecodeString(string(token))
	if err != nil {
		return 0, serviceerror.NewInvalidArgument("invalid next page token")
	}
	var pt pageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return 0, serviceerror.NewInvalidArgument("invalid next page token")
	}
	if pt.Query != query {
		return 0, serviceerror.NewInvalidArgument("page token does not match query")
	}
	return pt.Offset, nil
}

func copyStubStart(in stubStart) stubStart {
	out := in
	out.SearchAttributes = mapsCopyString(in.SearchAttributes)
	return out
}

func mapsCopyString(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mapsCopyEnum(in map[string]enumspb.IndexedValueType) map[string]enumspb.IndexedValueType {
	out := make(map[string]enumspb.IndexedValueType, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}
