package steps

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/internal/config"
)

// linearSteps is the conformance flow "linear" (WP-L5-04): four steps in
// order. The embedded profile proves it runs on the server kit started.
var linearSteps = []string{"s1", "s2", "s3", "s4"}

// linearRan records the steps the worker executed for the assertion.
type linearRan struct {
	mu  sync.Mutex
	ran []string
}

func (l *linearRan) step(_ context.Context, name string) (string, error) {
	l.mu.Lock()
	l.ran = append(l.ran, name)
	l.mu.Unlock()
	return name, nil
}

func (l *linearRan) calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ran...)
}

// linearFlow runs the four conformance steps in order.
func linearFlow(ctx workflow.Context) ([]string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
	ran := make([]string, 0, len(linearSteps))
	for _, name := range linearSteps {
		var out string
		if err := workflow.ExecuteActivity(ctx, "linearStep", name).Get(ctx, &out); err != nil {
			return nil, err
		}
		ran = append(ran, out)
	}
	return ran, nil
}

// flowTimeout bounds the SDK calls of the scenario. It is a deadline, not a
// sleep: a passing run returns as soon as the workflow completes.
const flowTimeout = 60 * time.Second

// registerEmbedded drives the embedded profile end to end: the kit process
// starts with an empty data directory, reports ready with the in-process
// Temporal server it started, and a conformance flow completes on that server
// (REQ-RT-003, RT-003-01).
func registerEmbedded(sc *godog.ScenarioContext) {
	sc.Step(`^a machine with no database, Temporal or NATS server running$`, func(ctx context.Context) error {
		// The default ports of the services the embedded profile must not
		// need are free, so a start that reached out to one would fail.
		for _, addr := range []string{"127.0.0.1:7233", "127.0.0.1:4222", "127.0.0.1:5432"} {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("%s is already in use: %w", addr, err)
			}
			_ = ln.Close()
		}
		w := world(ctx).Config
		if _, err := w.ensureDir(); err != nil {
			return err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		w.listen = ln.Addr().String()
		return ln.Close()
	})
	sc.Step(`^"kit serve --profile embedded" starts with an empty data directory$`, func(ctx context.Context) error {
		return world(ctx).Config.startServe("--profile", config.ProfileEmbedded)
	})
	sc.Step(`^the server reports ready$`, func(ctx context.Context) error {
		w := world(ctx).Config
		line, err := w.awaitReady()
		if err != nil {
			return err
		}
		if got := readyField(line, "runtime"); got != "temporal" {
			return fmt.Errorf("ready runtime = %q, want temporal", got)
		}
		if got := readyField(line, "temporal"); got != "embedded" {
			return fmt.Errorf("ready temporal implementation = %q, want embedded", got)
		}
		addr := readyField(line, "temporal.addr")
		if addr == "" {
			return fmt.Errorf("ready line %q does not name the embedded server address", line)
		}
		// The server must be reachable at the address the ready line names.
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("embedded temporal at %s: %w", addr, err)
		}
		return conn.Close()
	})
	sc.Step(`^a run of conformance flow "([^"]*)" completes$`, func(ctx context.Context, flow string) error {
		if flow != "linear" {
			return fmt.Errorf("unknown conformance flow %q", flow)
		}
		w := world(ctx).Config
		line, err := w.awaitReady()
		if err != nil {
			return err
		}
		addr := readyField(line, "temporal.addr")
		if addr == "" {
			return fmt.Errorf("ready line %q does not name the embedded server address", line)
		}
		c, err := client.Dial(client.Options{HostPort: addr, Namespace: "default"})
		if err != nil {
			return fmt.Errorf("dial %s: %w", addr, err)
		}
		defer c.Close()

		ran := &linearRan{}
		wk := worker.New(c, "conformance-linear", worker.Options{})
		wk.RegisterWorkflow(linearFlow)
		wk.RegisterActivityWithOptions(ran.step, activity.RegisterOptions{Name: "linearStep"})
		if err := wk.Start(); err != nil {
			return fmt.Errorf("worker: %w", err)
		}
		defer wk.Stop()

		runCtx, cancel := context.WithTimeout(context.Background(), flowTimeout)
		defer cancel()
		run, err := c.ExecuteWorkflow(runCtx, client.StartWorkflowOptions{
			ID:        "linear-embedded",
			TaskQueue: "conformance-linear",
		}, linearFlow)
		if err != nil {
			return fmt.Errorf("execute: %w", err)
		}
		var got []string
		if err := run.Get(runCtx, &got); err != nil {
			return fmt.Errorf("run: %w", err)
		}
		if len(got) != len(linearSteps) {
			return fmt.Errorf("run returned %v, want %v", got, linearSteps)
		}
		for i, name := range linearSteps {
			if got[i] != name {
				return fmt.Errorf("run returned %v, want %v", got, linearSteps)
			}
		}
		if calls := ran.calls(); len(calls) != len(linearSteps) {
			return fmt.Errorf("steps ran %v, want each of %v once", calls, linearSteps)
		}
		return nil
	})
}
