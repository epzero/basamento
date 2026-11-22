package steps

import (
	"errors"
	"fmt"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	operatorservice "go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

func listExec(t *testing.T, c client.Client, q string, page int32, token []byte) *workflowservice.ListWorkflowExecutionsResponse {
	t.Helper()
	resp, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace:     stubNamespace,
		Query:         q,
		PageSize:      page,
		NextPageToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func wfIDs(resp *workflowservice.ListWorkflowExecutionsResponse) []string {
	var out []string
	for _, e := range resp.GetExecutions() {
		out = append(out, e.GetExecution().GetWorkflowId())
	}
	return out
}

func seedListRuns(t *testing.T, stub *temporalStub, c client.Client, clk *testClock) {
	t.Helper()
	stub.PreRegisterSearchAttribute("BasamentoFlow", enumspb.INDEXED_VALUE_TYPE_KEYWORD)
	type spec struct {
		id, flow string
		status   enumspb.WorkflowExecutionStatus
	}
	specs := []spec{
		{"acme/a1", "pay", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED},
		{"acme/a2", "pay", enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING},
		{"acme/a3", "bill", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED},
		{"acme/cont", "pay", enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW},
		{"other/x", "pay", enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING},
	}
	for i, sp := range specs {
		clk.Add(time.Duration(i) * time.Second)
		opts := client.StartWorkflowOptions{
			ID:        sp.id,
			TaskQueue: "q",
			TypedSearchAttributes: temporal.NewSearchAttributes(
				temporal.NewSearchAttributeKeyKeyword("BasamentoFlow").ValueSet(sp.flow),
			),
		}
		mustStart(t, c, opts)
		if sp.status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			if err := stub.Finish(sp.id, sp.status); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTemporalStub_ListRuntimeQueryShapes(t *testing.T) {
	stub, clk := newStubForTest(t)
	c := dialStub(t, stub)
	seedListRuns(t, stub, c, clk)

	base := `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew"`

	t.Run("ne_only", func(t *testing.T) {
		got := wfIDs(listExec(t, c, base, 100, nil))
		want := []string{"acme/a3", "acme/a2", "acme/a1"}
		if len(got) != len(want) {
			t.Fatalf("got %v want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order %v want %v", got, want)
			}
		}
	})
	t.Run("ne_and_flow", func(t *testing.T) {
		q := base + ` AND BasamentoFlow = "pay"`
		got := wfIDs(listExec(t, c, q, 100, nil))
		want := []string{"acme/a2", "acme/a1"}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("ne_and_eq_running", func(t *testing.T) {
		q := base + ` AND ExecutionStatus = "Running"`
		got := wfIDs(listExec(t, c, q, 100, nil))
		if len(got) != 1 || got[0] != "acme/a2" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("ne_and_in_failed", func(t *testing.T) {
		q := base + ` AND ExecutionStatus IN ("Failed", "TimedOut")`
		got := wfIDs(listExec(t, c, q, 100, nil))
		if len(got) != 1 || got[0] != "acme/a3" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("ne_flow_eq", func(t *testing.T) {
		q := base + ` AND BasamentoFlow = "pay" AND ExecutionStatus = "Completed"`
		got := wfIDs(listExec(t, c, q, 100, nil))
		if len(got) != 1 || got[0] != "acme/a1" {
			t.Fatalf("got %v", got)
		}
	})
}

func TestTemporalStub_ListQueryDuplicatesRefusedRPC(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	stub.PreRegisterSearchAttribute("BasamentoFlow", enumspb.INDEXED_VALUE_TYPE_KEYWORD)
	prefix := `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew"`
	cases := []string{
		prefix + ` AND ExecutionStatus = "Running" AND ExecutionStatus = "Completed"`,
		prefix + ` AND ExecutionStatus != "Running" AND ExecutionStatus != "Failed"`,
		prefix + ` AND ExecutionStatus IN ("Running") AND ExecutionStatus IN ("Failed")`,
	}
	for _, q := range cases {
		_, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: stubNamespace,
			Query:     q,
		})
		if err == nil {
			t.Fatalf("expected error for %q", q)
		}
		var ia *serviceerror.InvalidArgument
		if !errors.As(err, &ia) {
			t.Fatalf("type %T", err)
		}
	}
}

func TestTemporalStub_ListINMatchesAllStatuses(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "acme/fail", TaskQueue: "q"})
	if err := stub.Finish("acme/fail", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED); err != nil {
		t.Fatal(err)
	}
	mustStart(t, c, client.StartWorkflowOptions{ID: "acme/timeout", TaskQueue: "q"})
	if err := stub.Finish("acme/timeout", enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT); err != nil {
		t.Fatal(err)
	}
	q := `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew" AND ExecutionStatus IN ("Failed", "TimedOut")`
	got := wfIDs(listExec(t, c, q, 100, nil))
	if len(got) != 2 {
		t.Fatalf("got %v want 2 ids", got)
	}
}

func TestTemporalStub_ListPagingAndTokens(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	for _, id := range []string{"p-1", "p-2", "p-3"} {
		mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
	}
	q := `WorkflowId STARTS_WITH "p-"`
	page1, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q, PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wfIDs(page1)[0] != "p-3" {
		t.Fatal("newest first page1")
	}
	tok0, err := encodePageToken("WorkflowId STARTS_WITH \"wrong\"", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q, NextPageToken: tok0,
	})
	if err == nil {
		t.Fatal("page token must match query")
	}
	_, err = c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q, NextPageToken: []byte("garbage"),
	})
	if err == nil {
		t.Fatal("garbage token")
	}
	big, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q, PageSize: 0,
	})
	if err != nil || len(big.Executions) != 3 {
		t.Fatalf("page size 0: %d err %v", len(big.Executions), err)
	}
}

func TestTemporalStub_ListWalksPages(t *testing.T) {
	stub, clk := newStubForTest(t)
	c := dialStub(t, stub)
	for i := 1; i <= 5; i++ {
		clk.Add(time.Second)
		mustStart(t, c, client.StartWorkflowOptions{ID: fmt.Sprintf("walk-p-%d", i), TaskQueue: "q"})
	}
	if err := stub.Finish("walk-p-3", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
		t.Fatal(err)
	}
	q := `WorkflowId STARTS_WITH "walk-p-" AND ExecutionStatus != "Completed"`
	var token []byte
	var pages [][]string
	for page := 0; page < 10; page++ {
		resp, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: stubNamespace, Query: q, PageSize: 2, NextPageToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids := wfIDs(resp)
		if len(pages) > 0 && fmt.Sprint(ids) == fmt.Sprint(pages[len(pages)-1]) {
			t.Fatal("page token did not advance listing")
		}
		pages = append(pages, ids)
		if len(resp.NextPageToken) == 0 {
			break
		}
		token = resp.NextPageToken
	}
	if len(pages) > 5 {
		t.Fatal("too many pages; token walk stuck")
	}
	want := [][]string{{"walk-p-5", "walk-p-4"}, {"walk-p-2", "walk-p-1"}}
	if len(pages) != 2 || fmt.Sprint(pages) != fmt.Sprint(want) {
		t.Fatalf("pages %v want %v", pages, want)
	}
	exact, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace,
		Query:     `WorkflowId STARTS_WITH "walk-p-" AND ExecutionStatus = "Completed"`,
		PageSize:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Executions) != 1 || len(exact.NextPageToken) != 0 {
		t.Fatalf("exact page %+v", exact)
	}
}

func TestTemporalStub_ListNegativePageSize(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	_, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace,
		Query:     `WorkflowId STARTS_WITH "x"`,
		PageSize:  -1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var ia *serviceerror.InvalidArgument
	if !errors.As(err, &ia) {
		t.Fatalf("type %T", err)
	}
}

func TestTemporalStub_ListNegativePageTokenOffset(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "tok-1", TaskQueue: "q"})
	q := `WorkflowId STARTS_WITH "tok-"`
	tok, err := encodePageToken(q, -1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q, NextPageToken: tok,
	})
	if err == nil {
		t.Fatal("negative offset")
	}
	tok2, err := encodePageToken(q, 99)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q, NextPageToken: tok2,
	})
	if err == nil {
		t.Fatal("past end offset")
	}
}

func TestTemporalStub_ListBasamentoFlowAfterAddSearchAttributes(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	q := `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew" AND BasamentoFlow = "pay"`
	_, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q,
	})
	if err == nil {
		t.Fatal("unregistered BasamentoFlow")
	}
	_, err = c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{
			"BasamentoFlow": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustStart(t, c, client.StartWorkflowOptions{
		ID: "acme/flow", TaskQueue: "q",
		TypedSearchAttributes: temporal.NewSearchAttributes(
			temporal.NewSearchAttributeKeyKeyword("BasamentoFlow").ValueSet("pay"),
		),
	})
	_, err = c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace, Query: q,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTemporalStub_ListHostileQueriesRefused(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	queries := []string{
		`WorkflowId STARTS_WITH "x" OR ExecutionStatus = "Running"`,
		`WorkflowId STARTS_WITH "x" ORDER BY StartTime`,
		"WorkflowId STARTS_WITH \"unclosed",
		`WorkflowId STARTS_WITH "x\" AND ExecutionStatus = "Running"`,
		"WorkflowId STARTS_WITH \"a\\b\"",
		"WorkflowId STARTS_WITH \"line\nbreak\"",
		`WorkflowId STARTS_WITH "x" and ExecutionStatus = "Running"`,
	}
	for _, q := range queries {
		_, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: stubNamespace, Query: q,
		})
		if err == nil {
			t.Fatalf("expected refusal for %q", q)
		}
		var ia *serviceerror.InvalidArgument
		if !errors.As(err, &ia) {
			t.Fatalf("type %T for %q", err, q)
		}
	}
}

func TestTemporalStub_ListQuotedOrderByInPrefix(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	id := `ORDER BY-is-wf-id`
	mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
	resp, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: stubNamespace,
		Query:     `WorkflowId STARTS_WITH "ORDER BY"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Executions) != 1 || resp.Executions[0].GetExecution().GetWorkflowId() != id {
		t.Fatalf("%v", resp.Executions)
	}
}

func TestTemporalStub_ListTieBreak(t *testing.T) {
	stub, clk := newStubForTest(t)
	c := dialStub(t, stub)
	same := clk.Now()
	for _, id := range []string{"t-a", "t-b"} {
		clk.Set(same)
		mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
	}
	resp := listExec(t, c, `WorkflowId STARTS_WITH "t-"`, 1, nil)
	if resp.Executions[0].GetExecution().GetWorkflowId() != "t-b" {
		t.Fatal("later insert wins tie")
	}
}
