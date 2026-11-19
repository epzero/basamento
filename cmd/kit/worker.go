package main

import (
	"context"
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
func runWorker(args []string) {
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
		os.Exit(2)
	}
	if *poolToken == "" || *sqlURL == "" || *dataDir == "" {
		fmt.Fprintln(os.Stderr, "kit worker: --pool-token, --sql-url and --data-dir are required")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	descriptors, invokers, err := loadWorkerProviders(*modelsFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kit worker:", err)
		os.Exit(1)
	}
	if len(descriptors) == 0 {
		fmt.Fprintln(os.Stderr, "kit worker: --models loaded no descriptors")
		os.Exit(1)
	}

	db, err := psql.Open(ctx, *sqlURL, migrations)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kit worker: sql store:", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()
	store, err := worker.Open(ctx, db, worker.Options{Clock: core.SystemClock{}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "kit worker: worker store:", err)
		os.Exit(1)
	}

	journal, err := worker.OpenJournal(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kit worker:", err)
		os.Exit(1)
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
		fmt.Fprintln(os.Stderr, "kit worker: join:", err)
		os.Exit(1)
	}
	fmt.Printf("kit worker: %s joined pool %s with %d descriptors\n", joined.ID, joined.Pool, len(joined.Descriptors))

	pending, err := client.Pending()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kit worker: journal:", err)
		os.Exit(1)
	}
	if len(pending) > 0 {
		fmt.Printf("kit worker: resuming %d invocations from the journal\n", len(pending))
	}

	runner := &worker.Runner{Client: client, Providers: invokers}
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = client.Leave(context.Background())
			return
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
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, inv := range leased {
			if err := runner.Run(ctx, inv); err != nil {
				fmt.Fprintf(os.Stderr, "kit worker: invocation %s: %v\n", inv.ClientRef, err)
			}
		}
		if *once {
			_ = client.Leave(context.Background())
			return
		}
		if len(leased) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
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
