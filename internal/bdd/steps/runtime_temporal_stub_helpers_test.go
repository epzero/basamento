package steps

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
)

// testClock is an injectable clock safe for concurrent stub RPCs and test edits.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(start time.Time) *testClock {
	return &testClock{now: start}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newStubForTest(t *testing.T) (*temporalStub, *testClock) {
	t.Helper()
	clk := newTestClock(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	stub, err := newTemporalStub(clk.Now, "1.32.2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stub.Close)
	return stub, clk
}

func dialStub(t *testing.T, stub *temporalStub) client.Client {
	t.Helper()
	c, err := client.Dial(stub.ClientOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func mustStart(t *testing.T, c client.Client, opts client.StartWorkflowOptions) client.WorkflowRun {
	t.Helper()
	run, err := c.ExecuteWorkflow(t.Context(), opts, noopWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func noopWorkflow(ctx workflow.Context) error { return nil }

func dummyActivity(ctx context.Context) error { return nil }
