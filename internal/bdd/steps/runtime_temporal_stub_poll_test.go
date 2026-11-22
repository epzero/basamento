package steps

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func dummyWf(ctx workflow.Context) error {
	return workflow.ExecuteActivity(ctx, dummyActivity).Get(ctx, nil)
}

func TestTemporalStub_WaitForPoll(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	w := worker.New(c, "wait-poll-q", worker.Options{})
	w.RegisterWorkflow(noopWorkflow)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	done := make(chan error, 1)
	go func() { done <- stub.WaitForPoll(t.Context()) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTemporalStub_WaitForPolls(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	w := worker.New(c, "poll-q", worker.Options{})
	w.RegisterWorkflow(dummyWf)
	w.RegisterActivity(dummyActivity)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	if err := stub.WaitForPolls(t.Context(), "workflow", "activity"); err != nil {
		t.Fatal(err)
	}
	if err := stub.WaitForPolls(t.Context(), "workflow", "activity"); err != nil {
		t.Fatal(err)
	}
	if err := stub.WaitForPolls(t.Context(), "bogus"); err == nil {
		t.Fatal("unknown kind")
	}
}

func TestTemporalStub_WaitForPollsContextCanceledBeforePoll(t *testing.T) {
	stub, _ := newStubForTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := stub.WaitForPolls(ctx, "workflow", "activity")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestTemporalStub_WaitForPollsBeforeWorker(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	done := make(chan error, 1)
	go func() {
		done <- stub.WaitForPolls(t.Context(), "workflow", "activity")
	}()
	w := worker.New(c, "early-q", worker.Options{})
	w.RegisterWorkflow(dummyWf)
	w.RegisterActivity(dummyActivity)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTemporalStub_WaitForPollBroadcast(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	w := worker.New(c, "bc-q", worker.Options{})
	w.RegisterWorkflow(dummyWf)
	w.RegisterActivity(dummyActivity)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	ch1 := make(chan error, 1)
	ch2 := make(chan error, 1)
	go func() { ch1 <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
	go func() { ch2 <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
	if err := <-ch1; err != nil {
		t.Fatal(err)
	}
	if err := <-ch2; err != nil {
		t.Fatal(err)
	}
}

func TestTemporalStub_WaitForPollsCloseUnblocks(t *testing.T) {
	stub, _ := newStubForTest(t)
	done := make(chan error, 1)
	go func() { done <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
	stub.Close()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected %v", err)
		}
	case <-t.Context().Done():
		t.Fatal("WaitForPolls did not unblock on Close")
	}
}

// CloseUnblocksPoll can only fail by hanging if Close does not unblock pending polls.
func TestTemporalStub_CloseUnblocksPoll(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	w := worker.New(c, "close-poll-q", worker.Options{})
	w.RegisterWorkflow(noopWorkflow)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	stub.Close()
	w.Stop()
}

func TestTemporalStub_WorkerPollAndClose(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	w := worker.New(c, "worker-q", worker.Options{})
	w.RegisterWorkflow(dummyWf)
	w.RegisterActivity(dummyActivity)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	if err := stub.WaitForPolls(t.Context(), "workflow", "activity"); err != nil {
		t.Fatal(err)
	}
	w.Stop()
	c.Close()
	reqs := stub.Requests()
	hasWF, hasAct, hasShut := false, false, false
	for _, r := range reqs {
		switch r {
		case "WorkflowService/PollWorkflowTaskQueue":
			hasWF = true
		case "WorkflowService/PollActivityTaskQueue":
			hasAct = true
		case "WorkflowService/ShutdownWorker":
			hasShut = true
		}
	}
	if !hasWF || !hasAct || !hasShut {
		t.Fatalf("reqs %v", reqs)
	}
}
