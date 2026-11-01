// Command bench measures the embedded Temporal server (BENCHMARKS.md).
//
//	bench perf  -dir <data dir>         latency, fan-out, concurrency, timers, updates, continue-as-new
//	bench crash -dir <data dir>         kill -9 mid-run, restart, verify recovery and replay
//	bench child -db <file> -port <n>    (internal) server + worker process used by crash
//
// -temporal-defaults compares against Temporal's own settings; -hostport
// measures an external server such as `temporal server start-dev`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/runtime/temporal/embedded"
)

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bench perf|crash|child ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "perf":
		err = perf(os.Args[2:])
	case "crash":
		err = crash(os.Args[2:])
	case "child":
		err = child(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func quietLogger() sdklog.Logger {
	return sdklog.NewStructuredLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func dial(hostPort string, timeout time.Duration) (client.Client, time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		c, err := client.Dial(client.Options{HostPort: hostPort, Namespace: "default", Logger: quietLogger()})
		if err == nil {
			if _, err = c.CheckHealth(context.Background(), &client.CheckHealthRequest{}); err == nil {
				return c, time.Since(start), nil
			}
			c.Close()
		}
		if time.Now().After(deadline) {
			return nil, 0, fmt.Errorf("server not healthy after %s: %w", timeout, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newWorker(c client.Client, stepLog string) worker.Worker {
	w := worker.New(c, taskQueue, worker.Options{})
	for _, wf := range []any{Single, Seq, Fan, Timer, Gate, Long, Chain} {
		w.RegisterWorkflow(wf)
	}
	w.RegisterActivity(Noop)
	w.RegisterActivity(&StepLog{Path: stepLog})
	return w
}

func run(ctx context.Context, c client.Client, id string, wf any, out any, args ...any) (time.Duration, error) {
	start := time.Now()
	r, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: taskQueue}, wf, args...)
	if err != nil {
		return 0, err
	}
	err = r.Get(ctx, out)
	return time.Since(start), err
}

func pct(ds []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

func report(name string, format string, args ...any) {
	fmt.Printf("%-28s %s\n", name, fmt.Sprintf(format, args...))
}

func perf(args []string) error {
	fs := flag.NewFlagSet("perf", flag.ExitOnError)
	dir := fs.String("dir", "", "data directory (fresh); empty = in-memory database")
	defaults := fs.Bool("temporal-defaults", false, "use Temporal's settings instead of ours")
	long := fs.Int("long", 3000, "activities in the continue-as-new test")
	external := fs.String("hostport", "", "benchmark an already running server instead of the embedded one")
	_ = fs.Parse(args)
	db := ""
	if *dir != "" {
		db = filepath.Join(*dir, "temporal.db")
	}
	ctx := context.Background()
	fmt.Printf("== temporal-defaults=%v db=%q\n", *defaults, db)

	t0 := time.Now()
	var srv *embedded.Server
	hostPort := *external
	if hostPort == "" {
		s, err := embedded.Start(embedded.Options{DBFile: db, TemporalDefaults: *defaults})
		if err != nil {
			return err
		}
		srv, hostPort = s, s.HostPort
	}
	c, _, err := dial(hostPort, 60*time.Second)
	if err != nil {
		return err
	}
	report("cold start (fresh db)", "%s", time.Since(t0).Round(time.Millisecond))
	w := newWorker(c, filepath.Join(os.TempDir(), "bench-steps.log"))
	if err := w.Start(); err != nil {
		return err
	}

	// Warm-up, then end-to-end latency of a one-activity workflow.
	var n int
	if _, err := run(ctx, c, "warm", Single, &n); err != nil {
		return err
	}
	var lat []time.Duration
	for i := 0; i < 50; i++ {
		d, err := run(ctx, c, fmt.Sprintf("single-%d", i), Single, &n)
		if err != nil {
			return err
		}
		lat = append(lat, d)
	}
	report("1-activity workflow e2e", "p50=%s p95=%s p99=%s (n=50)", pct(lat, .5).Round(time.Millisecond), pct(lat, .95).Round(time.Millisecond), pct(lat, .99).Round(time.Millisecond))

	d, err := run(ctx, c, "seq", Seq, &n, 200)
	if err != nil {
		return err
	}
	report("sequential activities", "200 in %s = %s/activity", d.Round(time.Millisecond), (d / 200).Round(time.Microsecond))
	if err := gaps(ctx, c, "seq"); err != nil {
		return err
	}

	d, err = run(ctx, c, "fan", Fan, &n, 200)
	if err != nil {
		return err
	}
	report("parallel fan-out", "200 in %s", d.Round(time.Millisecond))

	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out int
			if _, err := run(ctx, c, fmt.Sprintf("conc-%d", i), Seq, &out, 20); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		return fmt.Errorf("concurrent: %w", e)
	}
	report("20 workflows x 20 acts", "%s (400 activities)", time.Since(start).Round(time.Millisecond))

	d, err = run(ctx, c, "timer", Timer, nil, 2*time.Second)
	if err != nil {
		return err
	}
	report("2s durable timer", "fired after %s", d.Round(time.Millisecond))

	r, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "gate", TaskQueue: taskQueue}, Gate)
	if err != nil {
		return err
	}
	upd := func(who string) (time.Duration, error) {
		s := time.Now()
		h, err := c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{WorkflowID: "gate", UpdateName: "approve", Args: []any{who}, WaitForStage: client.WorkflowUpdateStageCompleted})
		if err != nil {
			return time.Since(s), err
		}
		var res string
		return time.Since(s), h.Get(ctx, &res)
	}
	if _, err := upd("bad"); err == nil {
		return errors.New("gate: invalid payload was accepted")
	}
	ud, err := upd("ana")
	if err != nil {
		return err
	}
	var who string
	if err := r.Get(ctx, &who); err != nil || who != "ana" {
		return fmt.Errorf("gate result %q: %w", who, err)
	}
	report("validated update (gate)", "invalid rejected; valid accepted in %s", ud.Round(time.Millisecond))

	d, err = run(ctx, c, "long", Long, &n, *long, 500, 0)
	if err != nil {
		return err
	}
	report("continue-as-new", "%d activities in batches of 500, %s", *long, d.Round(time.Millisecond))

	w.Stop()
	c.Close()
	if srv == nil {
		return nil
	}
	srv.Stop()
	if db == "" {
		report("peak RSS", "%s", peakRSS())
		return nil
	}

	t1 := time.Now()
	srv, err = embedded.Start(embedded.Options{DBFile: db, TemporalDefaults: *defaults})
	if err != nil {
		return err
	}
	c, _, err = dial(srv.HostPort, 60*time.Second)
	if err != nil {
		return err
	}
	report("warm start (existing db)", "%s", time.Since(t1).Round(time.Millisecond))
	c.Close()
	srv.Stop()

	if st, err := os.Stat(db); err == nil {
		report("sqlite file", "%.1f MiB after ~4,100 activities", float64(st.Size())/(1<<20))
	}
	report("peak RSS", "%s", peakRSS())
	return nil
}

// gaps prints the mean time between consecutive history event types, to locate latency.
func gaps(ctx context.Context, c client.Client, id string) error {
	it := c.GetWorkflowHistory(ctx, id, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var prev *historypb.HistoryEvent
	sum, cnt := map[string]time.Duration{}, map[string]int{}
	for it.HasNext() {
		e, err := it.Next()
		if err != nil {
			return err
		}
		if prev != nil {
			k := prev.EventType.String() + " -> " + e.EventType.String()
			sum[k] += e.EventTime.AsTime().Sub(prev.EventTime.AsTime())
			cnt[k]++
		}
		prev = e
	}
	for k, v := range sum {
		if cnt[k] > 10 {
			fmt.Printf("    %-80s %s\n", strings.ReplaceAll(k, "EVENT_TYPE_", ""), (v / time.Duration(cnt[k])).Round(100*time.Microsecond))
		}
	}
	return nil
}

func peakRSS() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "n/a"
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:"))
		}
	}
	return "n/a"
}

// child runs server + worker until killed; with -start it also starts the chain,
// with -wait it waits for the chain to finish and exits.
func child(args []string) error {
	fs := flag.NewFlagSet("child", flag.ExitOnError)
	db := fs.String("db", "", "sqlite file")
	port := fs.Int("port", 0, "frontend port")
	stepLog := fs.String("log", "", "step log file")
	startChain := fs.Bool("start", false, "start the chain workflow")
	wait := fs.Bool("wait", false, "wait for the chain to complete, then exit")
	defaults := fs.Bool("temporal-defaults", false, "use Temporal's settings instead of ours")
	wft := fs.Duration("wft-timeout", 2*time.Second, "workflow task timeout of the chain (Temporal default: 10s)")
	_ = fs.Parse(args)
	t0 := time.Now()
	srv, err := embedded.Start(embedded.Options{DBFile: *db, Port: *port, TemporalDefaults: *defaults})
	if err != nil {
		return err
	}
	c, _, err := dial(srv.HostPort, 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("ready %s\n", time.Since(t0).Round(time.Millisecond))
	w := newWorker(c, *stepLog)
	if err := w.Start(); err != nil {
		return err
	}
	ctx := context.Background()
	if *startChain {
		if _, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "chain", TaskQueue: taskQueue, WorkflowTaskTimeout: *wft}, Chain, 20); err != nil {
			return err
		}
		fmt.Println("started")
	}
	if *wait {
		var n int
		if err := c.GetWorkflow(ctx, "chain", "").Get(ctx, &n); err != nil {
			return err
		}
		fmt.Printf("completed %d %s\n", n, time.Since(t0).Round(time.Millisecond))
		if err := replay(ctx, c); err != nil {
			return err
		}
		w.Stop()
		c.Close()
		srv.Stop()
		return nil
	}
	select {}
}

func replay(ctx context.Context, c client.Client) error {
	it := c.GetWorkflowHistory(ctx, "chain", "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	h := &historypb.History{}
	for it.HasNext() {
		e, err := it.Next()
		if err != nil {
			return err
		}
		h.Events = append(h.Events, e)
	}
	ok := worker.NewWorkflowReplayer()
	ok.RegisterWorkflow(Chain)
	if err := ok.ReplayWorkflowHistory(quietLogger(), h); err != nil {
		return fmt.Errorf("replay with same code failed: %w", err)
	}
	bad := worker.NewWorkflowReplayer()
	bad.RegisterWorkflowWithOptions(ChainV2, workflow.RegisterOptions{Name: "Chain"})
	err := bad.ReplayWorkflowHistory(quietLogger(), h)
	if err == nil {
		return errors.New("replay with changed code did not detect nondeterminism")
	}
	fmt.Printf("replay ok (%d events); changed code detected: %.90s\n", len(h.Events), err.Error())
	return nil
}

func crash(args []string) error {
	fs := flag.NewFlagSet("crash", flag.ExitOnError)
	dir := fs.String("dir", "", "data directory (fresh)")
	defaults := fs.Bool("temporal-defaults", false, "use Temporal's settings instead of ours")
	wft := fs.Duration("wft-timeout", 2*time.Second, "workflow task timeout of the chain (Temporal default: 10s)")
	_ = fs.Parse(args)
	if *dir == "" {
		return errors.New("-dir required")
	}
	self, _ := os.Executable()
	childFlags := []string{fmt.Sprintf("-temporal-defaults=%v", *defaults), "-wft-timeout", wft.String()}
	db, stepLog := filepath.Join(*dir, "temporal.db"), filepath.Join(*dir, "steps.log")
	port := freePort()

	first := exec.Command(self, append([]string{"child", "-db", db, "-port", fmt.Sprint(port), "-log", stepLog, "-start"}, childFlags...)...)
	first.Stdout, first.Stderr = os.Stdout, os.Stderr
	if err := first.Start(); err != nil {
		return err
	}
	// Kill once roughly a third of the chain has completed.
	for countPrefix(stepLog, "done ") < 7 {
		time.Sleep(20 * time.Millisecond)
	}
	if err := first.Process.Signal(syscall.SIGKILL); err != nil {
		return err
	}
	_ = first.Wait()
	doneBefore := countPrefix(stepLog, "done ")
	fmt.Printf("killed with -9 after %d completed steps\n", doneBefore)

	t := time.Now()
	second := exec.Command(self, append([]string{"child", "-db", db, "-port", fmt.Sprint(port), "-log", stepLog, "-wait"}, childFlags...)...)
	second.Stdout, second.Stderr = os.Stdout, os.Stderr
	if err := second.Run(); err != nil {
		return fmt.Errorf("restarted process: %w", err)
	}
	fmt.Printf("restart to completion: %s\n", time.Since(t).Round(time.Millisecond))

	// Every step must be done exactly once; a step may start twice only if it
	// was in flight at the kill (at-least-once activities).
	starts, dones := map[string]int{}, map[string]int{}
	b, _ := os.ReadFile(stepLog)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			if f[0] == "start" {
				starts[f[1]]++
			} else {
				dones[f[1]]++
			}
		}
	}
	// The step in flight at the kill may run again (activities are at-least-once):
	// it can have finished its side effect before its completion reached the server.
	inFlight := fmt.Sprint(doneBefore - 1)
	reran := 0
	for i := 0; i < 20; i++ {
		k := fmt.Sprint(i)
		switch {
		case dones[k] == 0:
			return fmt.Errorf("step %d never completed", i)
		case dones[k] > 1 && k != inFlight && k != fmt.Sprint(doneBefore):
			return fmt.Errorf("step %d completed %d times but was not in flight at the kill", i, dones[k])
		}
		if starts[k] > 1 {
			reran++
		}
	}
	fmt.Printf("all 20 steps completed; completed steps never re-ran; %d in-flight step(s) re-executed (at-least-once)\n", reran)
	return nil
}

func countPrefix(path, prefix string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}
