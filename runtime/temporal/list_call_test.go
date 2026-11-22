package temporal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

var listFixedStart = time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)

func TestListLimitRules(t *testing.T) {
	if _, err := listLimit(-1); err == nil {
		t.Fatal("negative limit should be invalid_argument")
	}
	if n, err := listLimit(0); err != nil || n != 100 {
		t.Fatalf("limit 0: got %d err %v want 100", n, err)
	}
	if n, err := listLimit(5000); err != nil || n != 1000 {
		t.Fatalf("limit 5000: got %d err %v want clamped 1000", n, err)
	}
}

func TestListCursorCodecRoundTrip(t *testing.T) {
	tenant := core.TenantID("acme")
	q, err := listQuery(tenant, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := encodeListCursor(tenant, q, []byte{1, 2})
	if err != nil || tok == "" {
		t.Fatalf("encode: cursor=%q err=%v", tok, err)
	}
	page, err := parseListCursor(tok, tenant, q)
	if err != nil || !bytes.Equal(page, []byte{1, 2}) {
		t.Fatalf("round trip: page=%v err=%v want [1 2]", page, err)
	}
	if _, err := parseListCursor(tok, "beta", q); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("wrong tenant should reject, got %v", err)
	}
	if _, err := parseListCursor(tok, tenant, q+" "); err == nil {
		t.Fatalf("wrong query hash should reject")
	}
	if _, err := parseListCursor("!!!", tenant, q); err == nil {
		t.Fatalf("garbage cursor should reject")
	}
	if next, err := encodeListCursor(tenant, q, nil); err != nil || next != "" {
		t.Fatalf("empty server token: cursor=%q err=%v want empty", next, err)
	}
	emptyCurPayload, _ := json.Marshal(listCursorPayload{V: 1, T: "acme", Q: queryFingerprint(q), P: ""})
	if _, err := parseListCursor(base64.RawURLEncoding.EncodeToString(emptyCurPayload), tenant, q); err == nil {
		t.Fatalf("cursor with empty P should reject")
	}
}

func TestListDropsForeignAndUnmappableWithWarnLog(t *testing.T) {
	var buf bytes.Buffer
	fc := &fakeClient{}
	start := listFixedStart
	execs := []*workflowpb.WorkflowExecutionInfo{
		{Execution: &commonpb.WorkflowExecution{WorkflowId: "acme/r1"}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, StartTime: timestamppb.New(start)},
		{Execution: &commonpb.WorkflowExecution{WorkflowId: "beta/secret-run"}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, StartTime: timestamppb.New(start)},
		{Execution: &commonpb.WorkflowExecution{WorkflowId: "acme/r2"}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED, StartTime: timestamppb.New(start)},
	}
	fc.listResp = &workflowservice.ListWorkflowExecutionsResponse{Executions: execs, NextPageToken: []byte{0xab}}
	rt, err := New(Options{
		Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox(),
		Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(client.Options) (client.Client, error) { return fc, nil }
	ctx := core.WithTenant(context.Background(), "acme")
	out, cur, err := rt.List(ctx, runtime.RunQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(out) != 1 || out[0].Ref.RunID != "r1" || out[0].State != "running" {
		t.Fatalf("filtered results: got len=%d first=%+v", len(out), out)
	}
	if cur == "" {
		t.Fatal("expected non-empty cursor when server returns NextPageToken")
	}
	if strings.Contains(buf.String(), "beta/secret-run") {
		t.Fatal("warn log must not contain foreign workflow id")
	}
	if !strings.Contains(buf.String(), "dropped foreign") || !strings.Contains(buf.String(), "dropped unmappable") {
		t.Fatalf("expected drop warnings in log: %s", buf.String())
	}
	req := mustLastCall(fc, "list").ListReq
	if req.PageSize != 10 {
		t.Fatalf("PageSize: got %d want 10", req.PageSize)
	}
}

func TestListCursorPassThroughAndLastPageEmpty(t *testing.T) {
	tenant := core.TenantID("acme")
	ctx := core.WithTenant(context.Background(), "acme")

	fc := &fakeClient{}
	fc.listResp = &workflowservice.ListWorkflowExecutionsResponse{NextPageToken: []byte{0xab}}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(client.Options) (client.Client, error) { return fc, nil }
	_, cur, err := rt.List(ctx, runtime.RunQuery{Limit: 10})
	if err != nil || cur == "" {
		t.Fatalf("first page: cur=%q err=%v", cur, err)
	}

	listQ, _ := listQuery(tenant, "", "")
	page2, err := parseListCursor(cur, "acme", listQ)
	if err != nil {
		t.Fatalf("parse cursor: %v", err)
	}

	fc2 := &fakeClient{listResp: &workflowservice.ListWorkflowExecutionsResponse{}}
	rt2 := testControlRuntime(t, fc2)
	_, cur2, err := rt2.List(ctx, runtime.RunQuery{Cursor: cur})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	req := mustLastCall(fc2, "list").ListReq
	if !bytes.Equal(req.NextPageToken, page2) {
		t.Fatalf("NextPageToken: got %v want %v", req.NextPageToken, page2)
	}
	if cur2 != "" {
		t.Fatalf("last page cursor: got %q want empty", cur2)
	}
}

func TestListDefaultNamespaceAndPageSize(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	fc := &fakeClient{listResp: &workflowservice.ListWorkflowExecutionsResponse{}}
	rt := testControlRuntime(t, fc)
	if _, _, err := rt.List(ctx, runtime.RunQuery{}); err != nil {
		t.Fatalf("List failed: %v", err)
	}
	req := mustLastCall(fc, "list").ListReq
	if req.Namespace != client.DefaultNamespace {
		t.Fatalf("Namespace: got %q want %q", req.Namespace, client.DefaultNamespace)
	}
	if req.PageSize != 100 {
		t.Fatalf("PageSize for limit 0: got %d want 100", req.PageSize)
	}
}

func TestListRequestFiltersAndLimits(t *testing.T) {
	base := `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew"`
	cases := []struct {
		name                   string
		flow, state, wantQuery string
		limit                  int
		wantSize               int32
	}{
		{"no-filter", "", "", base, 0, 100},
		{"flow-only", "pay", "", base + ` AND BasamentoFlow = "pay"`, 5, 5},
		{"flow-and-state", "pay", "running", base + ` AND BasamentoFlow = "pay" AND ExecutionStatus = "Running"`, 5000, 1000},
	}
	ctx := core.WithTenant(context.Background(), "acme")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeClient{listResp: &workflowservice.ListWorkflowExecutionsResponse{}}
			rt := testControlRuntime(t, fc)
			if _, _, err := rt.List(ctx, runtime.RunQuery{Flow: tc.flow, State: tc.state, Limit: tc.limit}); err != nil {
				t.Fatalf("List failed: %v", err)
			}
			req := mustLastCall(fc, "list").ListReq
			if req.Query != tc.wantQuery {
				t.Fatalf("Query: got %q want %q", req.Query, tc.wantQuery)
			}
			if req.PageSize != tc.wantSize {
				t.Fatalf("PageSize: got %d want %d", req.PageSize, tc.wantSize)
			}
		})
	}
	t.Run("custom-namespace", func(t *testing.T) {
		rtNS, err := New(Options{
			Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox(),
			Client: client.Options{Namespace: "prod-ns"},
		})
		if err != nil {
			t.Fatal(err)
		}
		fcNS := &fakeClient{listResp: &workflowservice.ListWorkflowExecutionsResponse{}}
		rtNS.dial = func(client.Options) (client.Client, error) { return fcNS, nil }
		if _, _, err := rtNS.List(ctx, runtime.RunQuery{}); err != nil {
			t.Fatal(err)
		}
		if mustLastCall(fcNS, "list").ListReq.Namespace != "prod-ns" {
			t.Fatalf("Namespace: got %q want prod-ns", mustLastCall(fcNS, "list").ListReq.Namespace)
		}
	})
}

func TestListCursorRejectsBeforeClient(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	tenant := core.TenantID("acme")
	q, _ := listQuery(tenant, "pay", "running")
	validTok, _ := encodeListCursor(tenant, q, []byte{7})

	cases := []struct {
		name   string
		cursor string
		ctx    context.Context
		query  runtime.RunQuery
	}{
		{"garbage", "@@", ctx, runtime.RunQuery{Cursor: "@@", Flow: "pay", State: "running"}},
		{"wrong-version", mustCursorJSON(t, listCursorPayload{V: 2, T: "acme", Q: queryFingerprint(q), P: "Bw=="}), ctx, runtime.RunQuery{Cursor: mustCursorJSON(t, listCursorPayload{V: 2, T: "acme", Q: queryFingerprint(q), P: "Bw=="}), Flow: "pay", State: "running"}},
		{"non-json", base64.RawURLEncoding.EncodeToString([]byte("not-json")), ctx, runtime.RunQuery{Cursor: base64.RawURLEncoding.EncodeToString([]byte("not-json")), Flow: "pay", State: "running"}},
		{"bad-p", mustCursorJSON(t, listCursorPayload{V: 1, T: "acme", Q: queryFingerprint(q), P: "!!!"}), ctx, runtime.RunQuery{Cursor: mustCursorJSON(t, listCursorPayload{V: 1, T: "acme", Q: queryFingerprint(q), P: "!!!"}), Flow: "pay", State: "running"}},
		{"empty-p", mustCursorJSON(t, listCursorPayload{V: 1, T: "acme", Q: queryFingerprint(q), P: ""}), ctx, runtime.RunQuery{Cursor: mustCursorJSON(t, listCursorPayload{V: 1, T: "acme", Q: queryFingerprint(q), P: ""}), Flow: "pay", State: "running"}},
		{"other-tenant", validTok, core.WithTenant(context.Background(), "beta-co"), runtime.RunQuery{Cursor: validTok, Flow: "pay", State: "running"}},
		{"other-query", validTok, ctx, runtime.RunQuery{Cursor: validTok, Flow: "other", State: "running"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, fc := runtimeNoDial(t)
			_, _, err := rt.List(tc.ctx, tc.query)
			assertInvalidNoList(t, err, fc)
		})
	}
}

func mustCursorJSON(t *testing.T, p listCursorPayload) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func assertInvalidNoList(t *testing.T, err error, fc *fakeClient) {
	t.Helper()
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("expected invalid_argument cursor error, got %v", err)
	}
	if fc.clientOps() != 0 || fc.dialCount() != 0 {
		t.Fatalf("expected no client activity: dials=%d ops=%d", fc.dialCount(), fc.clientOps())
	}
}
