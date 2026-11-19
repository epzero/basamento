package embedded

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"go.temporal.io/server/common/dynamicconfig"
)

// testTimeout bounds every RPC in these tests. It is a deadline, not a sleep
// or a measurement: a passing test returns as soon as the server answers.
const testTimeout = 60 * time.Second

// linearSteps is the conformance flow "linear" (WP-L5-04): four activities
// chained in order. It is declared here because this module cannot import the
// root module's conformance suite.
var linearSteps = []string{"s1", "s2", "s3", "s4"}

// Echo records the name it ran under.
func Echo(_ context.Context, name string) (string, error) { return name, nil }

// Linear runs the four conformance steps in order and returns them.
func Linear(ctx workflow.Context) ([]string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
	ran := make([]string, 0, len(linearSteps))
	for _, name := range linearSteps {
		var out string
		if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &out); err != nil {
			return nil, err
		}
		ran = append(ran, out)
	}
	return ran, nil
}

// newWorker starts a worker on a task queue dedicated to this test.
func newWorker(t *testing.T, c client.Client, taskQueue string) worker.Worker {
	t.Helper()
	w := worker.New(c, taskQueue, worker.Options{})
	w.RegisterWorkflow(Linear)
	w.RegisterActivity(Echo)
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(w.Stop)
	return w
}

// TestLinearFlowCompletesInProcess starts the server on a file database and
// runs the linear conformance flow through it: the proof that a run completes
// with no external service (REQ-RT-003).
func TestLinearFlowCompletesInProcess(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "temporal.db")

	srv, err := Start(Options{DBFile: db})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)

	c, err := client.Dial(client.Options{HostPort: srv.HostPort, Namespace: "default", Logger: quietLogger{}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(c.Close)

	newWorker(t, c, "linear")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        "linear-1",
		TaskQueue: "linear",
	}, Linear)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var ran []string
	if err := run.Get(ctx, &ran); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(ran) != len(linearSteps) {
		t.Fatalf("steps ran = %v, want %v", ran, linearSteps)
	}
	for i, name := range linearSteps {
		if ran[i] != name {
			t.Fatalf("steps ran = %v, want %v", ran, linearSteps)
		}
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("database %s: %v", db, err)
	}
	if _, err := c.DescribeWorkflowExecution(ctx, run.GetID(), run.GetRunID()); err != nil {
		t.Fatalf("describe: %v", err)
	}
}

// TestRestartKeepsTheDatabase restarts the server on the same file and finds
// the completed run: the database is file-backed, not in-memory.
func TestRestartKeepsTheDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "temporal.db")

	first, err := Start(Options{DBFile: db})
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	c, err := client.Dial(client.Options{HostPort: first.HostPort, Namespace: "default", Logger: quietLogger{}})
	if err != nil {
		first.Stop()
		t.Fatalf("dial: %v", err)
	}
	newWorker(t, c, "linear")
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "linear-restart", TaskQueue: "linear"}, Linear)
	if err != nil {
		c.Close()
		first.Stop()
		t.Fatalf("execute: %v", err)
	}
	if err := run.Get(ctx, nil); err != nil {
		c.Close()
		first.Stop()
		t.Fatalf("run: %v", err)
	}
	runID := run.GetRunID()
	c.Close()
	first.Stop()

	second, err := Start(Options{DBFile: db})
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	t.Cleanup(second.Stop)
	c2, err := client.Dial(client.Options{HostPort: second.HostPort, Namespace: "default", Logger: quietLogger{}})
	if err != nil {
		t.Fatalf("dial after restart: %v", err)
	}
	t.Cleanup(c2.Close)
	desc, err := c2.DescribeWorkflowExecution(ctx, "linear-restart", runID)
	if err != nil {
		t.Fatalf("describe after restart: %v", err)
	}
	if got := desc.WorkflowExecutionInfo.Status; got != enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED {
		t.Fatalf("status after restart = %v, want completed", got)
	}
}

func TestSQLSettings(t *testing.T) {
	dir := t.TempDir()
	conf, err := sqlConfig(Options{DBFile: filepath.Join(dir, "temporal.db")})
	if err != nil {
		t.Fatalf("sqlConfig: %v", err)
	}
	want := map[string]string{
		"mode":         "rwc",
		"journal_mode": "wal",
		"synchronous":  "normal",
		"busy_timeout": "10000",
	}
	for key, value := range want {
		if got := conf.ConnectAttributes[key]; got != value {
			t.Errorf("ConnectAttributes[%q] = %q, want %q", key, got, value)
		}
	}
	if conf.MaxConns != 4 || conf.MaxIdleConns != 4 {
		t.Errorf("connections = (%d, %d), want (4, 4)", conf.MaxConns, conf.MaxIdleConns)
	}
	if _, err := os.Stat(filepath.Join(dir, "temporal.db")); err != nil {
		t.Errorf("schema was not set up: %v", err)
	}

	defaults, err := sqlConfig(Options{DBFile: filepath.Join(dir, "defaults.db"), TemporalDefaults: true})
	if err != nil {
		t.Fatalf("sqlConfig defaults: %v", err)
	}
	if _, ok := defaults.ConnectAttributes["journal_mode"]; ok {
		t.Error("TemporalDefaults must leave the journal mode to Temporal")
	}
	if defaults.MaxConns != 0 {
		t.Error("TemporalDefaults must leave connection counts to Temporal")
	}
}

func TestDynamicConfigSettings(t *testing.T) {
	c := dynConfig(false)
	if got := fmt.Sprint(c[dynamicconfig.MatchingNumTaskqueueReadPartitions.Key()]); got != "1" {
		t.Errorf("read partitions = %s, want 1", got)
	}
	if got := fmt.Sprint(c[dynamicconfig.MatchingNumTaskqueueWritePartitions.Key()]); got != "1" {
		t.Errorf("write partitions = %s, want 1", got)
	}
	if _, ok := dynConfig(true)[dynamicconfig.MatchingNumTaskqueueReadPartitions.Key()]; ok {
		t.Error("TemporalDefaults must leave partitions to Temporal")
	}
}

func TestOptionsUseAFreeLoopbackPort(t *testing.T) {
	conf, ports, err := serverConfig(Options{Port: 0})
	if err != nil {
		t.Fatalf("serverConfig: %v", err)
	}
	if ports["frontend"] == 0 || ports["history"] == 0 || ports["matching"] == 0 || ports["worker"] == 0 {
		t.Fatalf("ports = %v, want four assigned ports", ports)
	}
	if got := conf.Services["frontend"].RPC.BindOnIP; got != "127.0.0.1" {
		t.Errorf("bind address = %q, want loopback", got)
	}
	if conf.Persistence.NumHistoryShards != 1 {
		t.Errorf("history shards = %d, want 1", conf.Persistence.NumHistoryShards)
	}
}

func TestWaitReadyReportsTheTimeout(t *testing.T) {
	// Nothing listens on a port bound and released here; the poll must end
	// with the deadline and name the address.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	err = waitReady(addr, 50*time.Millisecond)
	if err == nil {
		t.Fatal("waitReady succeeded with nothing listening")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Fatalf("waitReady error = %v, want the address", err)
	}
}
