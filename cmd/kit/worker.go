package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/providers"
	"github.com/epzero/basamento/model/worker"
	psql "github.com/epzero/basamento/platform/sql"
)

// runWorker runs `kit worker`: it loads the providers from local manifests,
// joins one pool with a pool token and pulls invocations from its queue until
// it is signalled or, with --once, after one lease (ADR-0012).
//
// The first version wires the embedded profile: the platform and the worker
// share the SQL store through worker.LocalTransport. The cloud profile's
// generated gRPC client of basamento.worker.v1 is a later work package; the
// contract and the client loop are the same.
func runWorker(args []string) error {
	fs := flag.NewFlagSet("kit worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	poolToken := fs.String("pool-token", "", "pool token from the platform (required)")
	sqlURL := fs.String("sql-url", "", "SQL store URL of the embedded platform (required)")
	dataDir := fs.String("data-dir", "", "writable directory for the worker journal (required)")
	capacity := fs.Int("capacity", 1, "capacity in units")
	modelsFlag := fs.String("models", "", "comma-separated provider manifest files")
	labelsFlag := fs.String("labels", "", "comma-separated key=value labels, e.g. gpu=nvidia-a100,region=eu")
	once := fs.Bool("once", false, "lease and run at most one invocation, then leave")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *poolToken == "" || *sqlURL == "" || *dataDir == "" {
		return fmt.Errorf("kit worker: --pool-token, --sql-url and --data-dir are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	descriptors, invokers, err := loadWorkerProviders(*modelsFlag)
	if err != nil {
		return fmt.Errorf("kit worker: %w", err)
	}
	if len(descriptors) == 0 {
		return fmt.Errorf("kit worker: --models loaded no descriptors")
	}

	db, err := psql.Open(ctx, *sqlURL, migrations)
	if err != nil {
		return fmt.Errorf("kit worker: sql store: %w", err)
	}
	defer func() { _ = db.Close() }()
	store, err := worker.Open(ctx, db, worker.Options{Clock: core.SystemClock{}})
	if err != nil {
		return fmt.Errorf("kit worker: worker store: %w", err)
	}

	journal, err := worker.OpenJournal(*dataDir)
	if err != nil {
		return fmt.Errorf("kit worker: %w", err)
	}
	defer func() { _ = journal.Close() }()

	client := worker.NewClient(worker.LocalTransport{Store: store}, journal)
	joined, err := client.Join(ctx, worker.JoinRequest{
		PoolToken:       *poolToken,
		ProtocolVersion: worker.ProtocolVersion,
		Descriptors:     descriptors,
		Labels:          parseLabels(*labelsFlag),
		Capacity:        *capacity,
	})
	if err != nil {
		return fmt.Errorf("kit worker: join: %w", err)
	}
	fmt.Printf("kit worker: %s joined pool %s with %d descriptors\n", joined.ID, joined.Pool, len(joined.Descriptors))

	runner := &worker.Runner{Client: client, Providers: invokers}
	warm := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		warm = append(warm, d.ID)
	}
	loop := &workerLoop{
		client: client, runner: runner, store: store,
		heartbeatInterval: joined.HeartbeatInterval,
		warm:              warm,
		capacity:          *capacity, once: *once,
		stdout: os.Stdout, stderr: os.Stderr,
	}
	return loop.serve(ctx)
}

// workerLoop is the kit worker's run loop: heartbeat, replay the journal,
// lease, run, and drain on a signal or the platform's order.
type workerLoop struct {
	client *worker.Client
	runner *worker.Runner
	// store is the embedded platform's worker store. The loop sweeps and
	// spills for it: in the embedded profile this process is the platform.
	store             *worker.Store
	remotes           []worker.Remote
	heartbeatInterval time.Duration
	// warm is the models this worker has loaded, so the platform prefers
	// invocations that need nothing new (ADR-0012 §5).
	warm     []string
	capacity int
	once     bool
	stdout   io.Writer
	stderr   io.Writer

	// beats and sweeps override the tickers in tests; nil uses the real ones.
	// swept, when set, receives one value after each janitor pass.
	beats  <-chan time.Time
	sweeps <-chan time.Time
	swept  chan<- struct{}

	mu        sync.Mutex
	inUse     int
	draining  atomic.Bool
	fatal     atomic.Bool
	drained   chan struct{}
	drainOnce sync.Once
}

func (l *workerLoop) serve(ctx context.Context) error {
	if l.stdout == nil {
		l.stdout = io.Discard
	}
	if l.stderr == nil {
		l.stderr = io.Discard
	}
	// Invocations outlive a SIGTERM: the signal stops new leases and lets the
	// work in flight finish or be handed back (ADR-0012 §6).
	runCtx := context.WithoutCancel(ctx)
	l.drained = make(chan struct{})

	var wg sync.WaitGroup
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		l.heartbeat(runCtx, hbStop)
	}()
	sweepStop := make(chan struct{})
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		l.janitor(runCtx, sweepStop)
	}()

	if err := l.replay(runCtx); err != nil {
		_, _ = fmt.Fprintln(l.stderr, "kit worker: journal:", err)
	}

	leaseDone := make(chan struct{})
	go func() {
		defer close(leaseDone)
		l.lease(ctx, runCtx, &wg)
	}()

	<-leaseDone
	wg.Wait()
	close(hbStop)
	<-hbDone
	close(sweepStop)
	<-sweepDone

	_ = l.client.Leave(context.Background())
	if l.fatal.Load() {
		return errors.New("kit worker: no longer holds its leases")
	}
	return nil
}

func (l *workerLoop) heartbeat(ctx context.Context, stop <-chan struct{}) {
	tick := l.beats
	if tick == nil {
		interval := l.heartbeatInterval
		if interval <= 0 {
			interval = worker.DefaultHeartbeatInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-stop:
			return
		case <-tick:
			answer, err := l.client.Heartbeat(ctx, l.free())
			if err != nil {
				if fatalError(err) {
					l.fatal.Store(true)
					l.drain()
					return
				}
				_, _ = fmt.Fprintln(l.stderr, "kit worker: heartbeat:", err)
				continue
			}
			for _, ref := range answer.Cancel {
				if !l.runner.Cancel(ref) {
					// The invocation is still queued; the platform removes it
					// itself. Nothing local to cancel.
					continue
				}
			}
			if answer.Drain {
				l.drain()
			}
		}
	}
}

// janitorInterval is how often the embedded platform applies the queue's
// time-driven rules and its tenants' spill-over policy.
const janitorInterval = time.Second

// janitor sweeps queue timeouts, expired leases, redelivery, worker_lost and
// spill-over even while no worker asks for work (REQ-MODEL-014, -021, -022,
// -025, -032, -033).
func (l *workerLoop) janitor(ctx context.Context, stop <-chan struct{}) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A spill may be waiting on a remote provider; shutting down cancels it
	// rather than holding the process open.
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	tick := l.sweeps
	if tick == nil {
		ticker := time.NewTicker(janitorInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-stop:
			return
		case <-tick:
			if l.store == nil {
				continue
			}
			if err := l.store.Sweep(ctx); err != nil {
				_, _ = fmt.Fprintln(l.stderr, "kit worker: sweep:", err)
			}
			// The embedded deployment configures remote providers with the
			// run API (a later work package); with none, there is nothing to
			// spill and the call is a no-op.
			if _, err := l.store.Spill(ctx, l.remotes...); err != nil {
				_, _ = fmt.Fprintln(l.stderr, "kit worker: spill:", err)
			}
			if l.swept != nil {
				select {
				case l.swept <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (l *workerLoop) lease(ctx context.Context, runCtx context.Context, wg *sync.WaitGroup) {
	for {
		if ctx.Err() != nil {
			l.drain()
		}
		if l.draining.Load() || l.fatal.Load() {
			return
		}
		free := l.free()
		if free <= 0 {
			select {
			case <-ctx.Done():
				l.drain()
				return
			case <-l.drained:
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		wait := 30 * time.Second
		if l.once {
			wait = 0
		}
		leased, err := l.client.LeaseOnce(ctx, free, l.warm, 30*time.Second, wait)
		if err != nil {
			_, _ = fmt.Fprintln(l.stderr, "kit worker: lease:", err)
			if fatalError(err) {
				l.fatal.Store(true)
				l.drain()
				return
			}
			select {
			case <-ctx.Done():
				l.drain()
				return
			case <-l.drained:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if len(leased) == 0 {
			if l.once {
				l.drain()
				return
			}
			select {
			case <-ctx.Done():
				l.drain()
				return
			case <-l.drained:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		ran := false
		for _, inv := range leased {
			if l.once && ran {
				// --once runs at most one invocation even when the platform
				// hands back several for the free units.
				_ = l.client.Release(context.WithoutCancel(ctx), inv.ClientRef, true, time.Time{})
				continue
			}
			if ctx.Err() != nil {
				l.drain()
			}
			if l.draining.Load() || l.fatal.Load() {
				// A signal arrived between the lease and the start: hand the
				// lease back unused instead of abandoning it.
				_ = l.client.Release(context.WithoutCancel(ctx), inv.ClientRef, true, time.Time{})
				continue
			}
			ran = true
			l.addUnits(inv.Units)
			wg.Add(1)
			go func(inv worker.Invocation) {
				defer wg.Done()
				defer l.addUnits(-inv.Units)
				if err := l.runner.Run(runCtx, inv); err != nil {
					_, _ = fmt.Fprintf(l.stderr, "kit worker: invocation %s: %v\n", inv.ClientRef, err)
				}
			}(inv)
		}
		if l.once {
			l.drain()
			return
		}
	}
}

// drain stops the loop taking new work: the leases it holds run to their end
// and the process leaves (ADR-0012 §6).
func (l *workerLoop) drain() {
	l.draining.Store(true)
	l.drainOnce.Do(func() { close(l.drained) })
}

// fatalError says whether a heartbeat or lease error means this worker can
// never take work again: the platform no longer holds its leases, its
// credential was revoked, or its worker row is gone (REQ-MODEL-024,
// REQ-MODEL-030).
func fatalError(err error) bool {
	switch core.CodeOf(err) {
	case worker.CodeLeaseLost, core.CodeUnauthenticated, core.CodeNotFound:
		return true
	}
	return false
}

// replay resumes the leases and redelivers the results the journal kept
// (ADR-0012 §7). A lease the platform no longer holds is recorded done: its
// outcome is already settled.
func (l *workerLoop) replay(ctx context.Context) error {
	pending, err := l.client.Pending()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(l.stdout, "kit worker: resuming %d invocations from the journal\n", len(pending))
	for _, p := range pending {
		if p.Kind == "result" {
			err := l.client.Finish(ctx, p.ClientRef, p.State, p.Result, core.Code(p.ErrorCode), p.CostMpts)
			if err == nil || core.CodeOf(err) == worker.CodeLeaseLost {
				l.markDone(p.ClientRef)
				continue
			}
			_, _ = fmt.Fprintf(l.stderr, "kit worker: redeliver %s: %v\n", p.ClientRef, err)
			continue
		}
		if err := l.client.Resume(ctx, p.ClientRef); err != nil {
			if core.CodeOf(err) == worker.CodeLeaseLost {
				l.markDone(p.ClientRef)
				continue
			}
			_, _ = fmt.Fprintf(l.stderr, "kit worker: resume %s: %v\n", p.ClientRef, err)
			continue
		}
		inv := worker.Invocation{
			ClientRef: p.ClientRef, Model: p.Model, Attempt: p.Attempt, Request: p.Request,
		}
		var runErr error
		if p.ProviderHandle != "" {
			runErr = l.runner.RunResumed(ctx, inv, p.ProviderHandle)
		} else {
			runErr = l.runner.Run(ctx, inv)
		}
		if runErr != nil {
			_, _ = fmt.Fprintf(l.stderr, "kit worker: invocation %s: %v\n", p.ClientRef, runErr)
		}
	}
	return nil
}

func (l *workerLoop) markDone(clientRef string) {
	if j := l.client.Journal(); j != nil {
		_ = j.Record(worker.JournalEntry{Kind: "done", ClientRef: clientRef})
	}
}

func (l *workerLoop) free() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	free := l.capacity - l.inUse
	if free < 0 {
		return 0
	}
	return free
}

func (l *workerLoop) addUnits(units int) {
	if units <= 0 {
		units = 1
	}
	l.mu.Lock()
	l.inUse += units
	l.mu.Unlock()
}

func loadWorkerProviders(list string) ([]worker.Descriptor, map[string]model.Invoker, error) {
	var descriptors []worker.Descriptor
	invokers := map[string]model.Invoker{}
	if strings.TrimSpace(list) == "" {
		return nil, invokers, nil
	}
	for _, path := range strings.Split(list, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		m, err := providers.LoadManifest(path)
		if err != nil {
			return nil, nil, err
		}
		descs, err := m.Descriptors()
		if err != nil {
			return nil, nil, err
		}
		var invoker model.Invoker
		switch {
		case m.CLI != nil:
			invoker, err = providers.NewCLI(m, providers.CLIOptions{})
		case m.HTTP != nil:
			invoker, err = providers.NewHTTP(m, providers.HTTPOptions{Credentials: envCredentials{}})
		default:
			err = fmt.Errorf("manifest %q has no driver", path)
		}
		if err != nil {
			return nil, nil, err
		}
		for i, d := range descs {
			units := 1
			restart := false
			if i < len(m.Models) {
				if m.Models[i].Units > 0 {
					units = m.Models[i].Units
				}
				restart = m.Models[i].Restart == "safe"
			}
			descriptors = append(descriptors, worker.Descriptor{Descriptor: d, Units: units, RestartSafe: restart})
			invokers[d.ID] = invoker
		}
	}
	return descriptors, invokers, nil
}

func parseLabels(list string) map[string]string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(list, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) == 2 && parts[0] != "" {
			out[parts[0]] = parts[1]
		}
	}
	return out
}

// envCredentials resolves a cred:// reference from the worker's own
// environment: BASAMENTO_CRED_<NAME>, the last path segment uppercased with
// non-alphanumerics replaced by underscores. Vendor credentials stay on the
// worker; the platform never sees them (ADR-0012 §10).
type envCredentials struct{}

func (envCredentials) Resolve(_ context.Context, ref string) ([]byte, error) {
	name := strings.TrimPrefix(ref, "cred://")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ToUpper(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	value, ok := os.LookupEnv("BASAMENTO_CRED_" + b.String())
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "credential is not in the worker environment", map[string]any{"ref": ref})
	}
	return []byte(value), nil
}
