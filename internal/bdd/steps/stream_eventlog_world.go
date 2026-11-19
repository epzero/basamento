package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/internal/bdd/sqltest"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
	"github.com/epzero/basamento/stream/eventlog"
)

var eventlogNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

type eventlogWorld struct {
	tenant string

	dir       string
	pgCleanup func()
	store     *psql.Store
	log       *eventlog.Log
	clock     *coretest.FakeClock
	bus       *eventBus
	relay     *eventlog.Relay

	crashAt     string
	crashCode   core.Code
	relayStopAt string

	lastErr     error
	lastRead    []eventlog.Event
	lastReadErr error
}

func newEventlogWorld() *eventlogWorld {
	return &eventlogWorld{tenant: "acme"}
}

func scenarioUsesEventlog(scn *godog.Scenario) bool {
	if filepath.Base(scn.Uri) == "event-log.feature" {
		return true
	}
	for _, tag := range scn.Tags {
		if tag.Name == "@wp:WP-L6-01" {
			return true
		}
	}
	return false
}

func (w *eventlogWorld) tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

// open prepares the SQL store, the log, the recording bus and the relay of the
// scenario. It runs once per scenario, after the tenant step.
func (w *eventlogWorld) open(ctx context.Context) error {
	if w.store != nil {
		return nil
	}
	url, err := w.databaseURL(ctx)
	if err != nil {
		return err
	}
	store, err := psql.Open(ctx, url, eventlog.Migrations())
	if err != nil {
		return err
	}
	w.store = store
	w.clock = coretest.NewFakeClock(eventlogNow)
	log, err := eventlog.New(store, eventlog.Options{Clock: w.clock, Fault: w.fault})
	if err != nil {
		return err
	}
	w.log = log
	w.bus = &eventBus{}
	relay, err := eventlog.NewRelay(log, w.bus, eventlog.RelayOptions{
		Clock: w.clock,
		Fault: w.relayFault,
		After: w.clock.After,
	})
	if err != nil {
		return err
	}
	w.relay = relay
	return nil
}

func (w *eventlogWorld) databaseURL(ctx context.Context) (string, error) {
	if os.Getenv("BDD_SQL_PROFILE") == "cloud" && sqltest.PostgresConfigured() {
		url, cleanup, err := sqltest.CreateDatabase(ctx)
		if err != nil {
			return "", err
		}
		w.pgCleanup = cleanup
		return url, nil
	}
	dir, err := os.MkdirTemp("", "eventlog-sqlite-*")
	if err != nil {
		return "", err
	}
	w.dir = dir
	return "sqlite:///" + filepath.Join(dir, "kit.db"), nil
}

func (w *eventlogWorld) teardown() {
	if w.store != nil {
		_ = w.store.Close()
		w.store = nil
	}
	if w.pgCleanup != nil {
		w.pgCleanup()
		w.pgCleanup = nil
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
		w.dir = ""
	}
}

func (w *eventlogWorld) fault(point string) error {
	return w.stopAt(point, w.crashAt, w.crashCode)
}

func (w *eventlogWorld) relayFault(point string) error {
	return w.stopAt(point, w.relayStopAt, core.CodeInternal)
}

func (w *eventlogWorld) stopAt(point, configured string, code core.Code) error {
	if configured == "" || point != configured {
		return nil
	}
	if code == "" {
		code = core.CodeInternal
	}
	return core.NewError(code, "stopped at "+point, nil)
}

func (w *eventlogWorld) ensureRun(ctx context.Context, tenant, runID, state string) error {
	err := w.log.CreateRun(w.tenantCtx(tenant), runID, state)
	if err != nil && core.CodeOf(err) != core.CodeConflict {
		return err
	}
	return nil
}

func (w *eventlogWorld) appendTypes(ctx context.Context, tenant, runID, state string, types []string) error {
	events := make([]eventlog.NewEvent, len(types))
	for i, typ := range types {
		events[i] = eventlog.NewEvent{Type: typ, Data: json.RawMessage(`{"n":` + strconv.Itoa(i) + `}`)}
	}
	_, err := w.log.AppendChange(w.tenantCtx(tenant), runID, state, events)
	return err
}

// eventBus is the recording platform.Bus of the durable-event scenarios. It
// accepts a configurable number of publishes, then reports unavailable until it
// is made reachable again.
type eventBus struct {
	mu        sync.Mutex
	envs      []publishedEnvelope
	accepted  int
	failAfter int
	down      bool
}

type publishedEnvelope struct {
	subject string
	env     eventlog.Envelope
}

func (b *eventBus) Publish(ctx context.Context, subject string, data []byte) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, core.NewError(core.CodeCancelled, "cancelled", nil)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down || (b.failAfter > 0 && b.accepted >= b.failAfter) {
		return 0, core.NewError(core.CodeUnavailable, "bus unreachable", nil)
	}
	var env eventlog.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return 0, core.NewError(core.CodeInternal, "bad envelope", nil)
	}
	b.accepted++
	b.envs = append(b.envs, publishedEnvelope{subject: subject, env: env})
	return uint64(b.accepted), nil
}

func (b *eventBus) Subscribe(context.Context, string, uint64) (<-chan platform.Message, error) {
	return nil, core.NewError(core.CodeUnsupported, "not used by the event log scenarios", nil)
}

func (b *eventBus) LastSeq(context.Context, string) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return uint64(b.accepted), nil
}

func (b *eventBus) Purge(context.Context, string) error { return nil }

func (b *eventBus) setUnreachableAfter(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failAfter = n
	b.down = false
}

func (b *eventBus) setReachable() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failAfter = 0
	b.down = false
}

func (b *eventBus) setDown(down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = down
}

func (b *eventBus) forRun(runID string) []eventlog.Envelope {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []eventlog.Envelope
	for _, m := range b.envs {
		if m.env.RunID == runID {
			out = append(out, m.env)
		}
	}
	return out
}

func (b *eventBus) subjectsFor(runID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, m := range b.envs {
		if m.env.RunID == runID {
			out = append(out, m.subject)
		}
	}
	return out
}

var errEventlogNoWorld = errors.New("eventlog world not opened")
