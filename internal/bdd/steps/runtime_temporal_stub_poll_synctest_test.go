package steps

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"go.temporal.io/api/workflowservice/v1"
)

var stubClockStart = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func TestTemporalStub_WaitForPollsNeedsAllKindsSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub, err := newTemporalStub(newTestClock(stubClockStart).Now, "1.32.2")
		if err != nil {
			t.Fatal(err)
		}
		defer stub.Close()
		pollCtx, pollHold := context.WithCancel(t.Context())
		defer pollHold()
		go func() { _, _ = stub.PollWorkflowTaskQueue(pollCtx, &workflowservice.PollWorkflowTaskQueueRequest{}) }()
		synctest.Wait()
		done := make(chan error, 1)
		go func() { done <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("must not complete before activity poll: %v", err)
		default:
		}
		go func() { _, _ = stub.PollActivityTaskQueue(pollCtx, &workflowservice.PollActivityTaskQueueRequest{}) }()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestTemporalStub_WaitForPollsMissingKindCanceledSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub, err := newTemporalStub(newTestClock(stubClockStart).Now, "1.32.2")
		if err != nil {
			t.Fatal(err)
		}
		defer stub.Close()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- stub.WaitForPolls(ctx, "workflow", "activity") }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		err = <-done
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestTemporalStub_WaitForPollsCloseSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub, err := newTemporalStub(newTestClock(stubClockStart).Now, "1.32.2")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
		synctest.Wait()
		stub.Close()
		synctest.Wait()
		err = <-done
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestTemporalStub_WaitForPollSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub, err := newTemporalStub(newTestClock(stubClockStart).Now, "1.32.2")
		if err != nil {
			t.Fatal(err)
		}
		defer stub.Close()
		pollCtx, pollHold := context.WithCancel(t.Context())
		defer pollHold()
		go func() { _, _ = stub.PollActivityTaskQueue(pollCtx, &workflowservice.PollActivityTaskQueueRequest{}) }()
		synctest.Wait()
		if err := stub.WaitForPoll(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTemporalStub_WaitForPollsBroadcastSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub, err := newTemporalStub(newTestClock(stubClockStart).Now, "1.32.2")
		if err != nil {
			t.Fatal(err)
		}
		defer stub.Close()
		pollCtx, pollHold := context.WithCancel(t.Context())
		defer pollHold()
		go func() { _, _ = stub.PollWorkflowTaskQueue(pollCtx, &workflowservice.PollWorkflowTaskQueueRequest{}) }()
		synctest.Wait()
		ch1, ch2, ch3 := make(chan error, 1), make(chan error, 1), make(chan error, 1)
		go func() { ch1 <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
		go func() { ch2 <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
		go func() { ch3 <- stub.WaitForPolls(t.Context(), "workflow", "activity") }()
		synctest.Wait()
		go func() { _, _ = stub.PollActivityTaskQueue(pollCtx, &workflowservice.PollActivityTaskQueueRequest{}) }()
		synctest.Wait()
		for _, ch := range []chan error{ch1, ch2, ch3} {
			if err := <-ch; err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestTemporalStub_WaitForPollsAlreadySeenSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub, err := newTemporalStub(newTestClock(stubClockStart).Now, "1.32.2")
		if err != nil {
			t.Fatal(err)
		}
		defer stub.Close()
		pollCtx, pollHold := context.WithCancel(t.Context())
		defer pollHold()
		go func() { _, _ = stub.PollWorkflowTaskQueue(pollCtx, &workflowservice.PollWorkflowTaskQueueRequest{}) }()
		go func() { _, _ = stub.PollActivityTaskQueue(pollCtx, &workflowservice.PollActivityTaskQueueRequest{}) }()
		synctest.Wait()
		if err := stub.WaitForPolls(t.Context(), "workflow", "activity"); err != nil {
			t.Fatal(err)
		}
	})
}
