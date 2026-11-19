package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/providers"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/model/worker/scale"
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
	if len(args) > 0 && args[0] == "describe" {
		return runWorkerDescribe(args[1:])
	}
	fs := flag.NewFlagSet("kit worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	poolToken := fs.String("pool-token", "", "pool token or single-use join token from the platform (required)")
	sqlURL := fs.String("sql-url", "", "SQL store URL of the embedded platform (required)")
	dataDir := fs.String("data-dir", "", "writable directory for the worker journal (required)")
	capacity := fs.Int("capacity", 1, "capacity in units")
	modelsFlag := fs.String("models", "", "comma-separated provider manifest files")
	labelsFlag := fs.String("labels", "", "comma-separated key=value labels, e.g. gpu=nvidia-a100,region=eu")
	once := fs.Bool("once", false, "lease and run at most one invocation, then leave")
	idleExit := fs.Duration("idle-exit", 0, "leave after this long without a lease (0 disables it)")
	maxLifetime := fs.Duration("max-lifetime", 0, "drain and leave after this long in total (0 disables it)")
	maxInvocations := fs.Int("max-invocations", 0, "leave after this many invocations (0 disables it)")
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
	clock := core.SystemClock{}
	tokens, err := scale.OpenTokens(ctx, db, scale.Options{Clock: clock})
	if err != nil {
		return fmt.Errorf("kit worker: template store: %w", err)
	}
	store, err := worker.Open(ctx, db, worker.Options{
		Clock: clock, LaunchTokens: tokens, Templates: tokens,
	})
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

	pending, err := client.Pending()
	if err != nil {
		return fmt.Errorf("kit worker: journal: %w", err)
	}
	if len(pending) > 0 {
		fmt.Printf("kit worker: resuming %d invocations from the journal\n", len(pending))
	}

	runner := &worker.Runner{Client: client, Providers: invokers}
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	policy := scale.ExitPolicy{IdleExit: *idleExit, MaxLifetime: *maxLifetime, MaxInvocations: *maxInvocations}
	started := clock.Now()
	lastWork := started
	ran := 0
	for {
		if policy.Leave(clock.Now(), started, lastWork, ran) {
			fmt.Println("kit worker: leaving by its exit policy")
			_ = client.Leave(context.Background())
			return nil
		}
		select {
		case <-ctx.Done():
			_ = client.Leave(context.Background())
			return nil
		case <-heartbeat.C:
			if _, err := client.Heartbeat(ctx, *capacity); err != nil {
				fmt.Fprintln(os.Stderr, "kit worker: heartbeat:", err)
			}
			continue
		default:
		}
		leased, err := client.LeaseOnce(ctx, *capacity, nil, 30*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kit worker: lease:", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		for _, inv := range leased {
			if err := runner.Run(ctx, inv); err != nil {
				fmt.Fprintf(os.Stderr, "kit worker: invocation %s: %v\n", inv.ClientRef, err)
			}
			ran++
		}
		if len(leased) > 0 {
			lastWork = clock.Now()
		}
		if *once {
			_ = client.Leave(context.Background())
			return nil
		}
		if len(leased) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}

// runWorkerDescribe prints what one worker serves, in the form a worker
// template stores it: descriptors, labels and capacity. An image exports this
// once, so a launched worker can be compared with its template at join
// (ADR-0012 §12).
func runWorkerDescribe(args []string) error {
	fs := flag.NewFlagSet("kit worker describe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelsFlag := fs.String("models", "", "comma-separated provider manifest files")
	labelsFlag := fs.String("labels", "", "comma-separated key=value labels")
	capacity := fs.Int("capacity", 1, "capacity in units")
	if err := fs.Parse(args); err != nil {
		return err
	}
	descriptors, _, err := loadWorkerProviders(*modelsFlag)
	if err != nil {
		return fmt.Errorf("kit worker describe: %w", err)
	}
	if len(descriptors) == 0 {
		return fmt.Errorf("kit worker describe: --models loaded no descriptors")
	}
	out := struct {
		Descriptors []worker.Descriptor `json:"descriptors"`
		Labels      map[string]string   `json:"labels,omitempty"`
		Capacity    int                 `json:"capacity"`
	}{Descriptors: descriptors, Labels: parseLabels(*labelsFlag), Capacity: *capacity}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
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
