package steps

import (
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

func TestTemporalStub_GetSystemInfoAndSetVersion(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "reqs", TaskQueue: "q"})
	if !containsReq(stub.Requests(), "WorkflowService/GetSystemInfo") {
		t.Fatal("GetSystemInfo must be in Requests()")
	}
	resp, err := c.WorkflowService().GetSystemInfo(t.Context(), &workflowservice.GetSystemInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetServerVersion() != "1.32.2" {
		t.Fatalf("version %q", resp.GetServerVersion())
	}
	stub.SetVersion("9.9.9")
	resp2, err := c.WorkflowService().GetSystemInfo(t.Context(), &workflowservice.GetSystemInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp2.GetServerVersion() != "9.9.9" {
		t.Fatal("SetVersion")
	}
}

func containsReq(reqs []string, want string) bool {
	for _, r := range reqs {
		if r == want {
			return true
		}
	}
	return false
}

func TestTemporalStub_AccessorsReturnCopies(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "a", TaskQueue: "q"})
	reqs := stub.Requests()
	reqs[0] = "mutated"
	if stub.Requests()[0] == "mutated" {
		t.Fatal("Requests")
	}
	starts := stub.Starts()
	starts[0].WorkflowID = "mut"
	if stub.Starts()[0].WorkflowID != "a" {
		t.Fatal("Starts")
	}
	execs := stub.Executions()
	execs[0].WorkflowID = "mut"
	if stub.Executions()[0].WorkflowID != "a" {
		t.Fatal("Executions")
	}
	sa := stub.SearchAttributes()
	sa["x"] = enumspb.INDEXED_VALUE_TYPE_KEYWORD
	if len(stub.SearchAttributes()) != 0 {
		t.Fatal("SearchAttributes")
	}
}
