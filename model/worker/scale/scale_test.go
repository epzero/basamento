package scale_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/model/worker/scale"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/launcher"
	psql "github.com/epzero/basamento/platform/sql"
)

var at = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

type seqTokens struct {
	mu sync.Mutex
	n  int
}

func (s *seqTokens) next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("t%d", s.n)
}

type fixture struct {
	ctx    context.Context
	db     *psql.Store
	store  *worker.Store
	tokens *scale.TokenStore
	scaler *scale.Scaler
	sim    *launcher.Sim
	clock  *coretest.FakeClock
	token  *seqTokens
	seq    int
	joined *joinedWorker
}

type joinedWorker struct {
	worker     worker.Worker
	credential string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := psql.Open(ctx, "sqlite:///"+dir+"/worker.db", fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := coretest.NewFakeClock(at)
	token := &seqTokens{}
	tokens, err := scale.OpenTokens(ctx, db, scale.Options{Clock: clock, IDs: &coretest.SeqIds{}, NewToken: token.next})
	if err != nil {
		t.Fatal(err)
	}
	store, err := worker.Open(ctx, db, worker.Options{
		Clock: clock, IDs: &coretest.SeqIds{}, LaunchTokens: tokens, Templates: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	sim := launcher.NewSim()
	scaler, err := scale.NewScaler(store, tokens, scale.ScalerOptions{
		Clock: clock, Launchers: map[string]platform.Launcher{"sim": sim},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{ctx: ctx, db: db, store: store, tokens: tokens, scaler: scaler, sim: sim, clock: clock, token: token}
}

func (f *fixture) pool(t *testing.T, id string, tenant core.TenantID) {
	t.Helper()
	if _, err := f.store.CreatePool(f.ctx, worker.PoolSpec{
		ID: id, Tenant: tenant, Lease: 5 * time.Minute, Grace: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) template(t *testing.T, tmpl scale.Template) {
	t.Helper()
	if tmpl.Launcher == "" {
		tmpl.Launcher = "sim"
	}
	if err := f.tokens.SaveTemplate(f.ctx, tmpl); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) reconfigure(t *testing.T, opts scale.ScalerOptions) {
	t.Helper()
	opts.Clock = f.clock
	opts.Launchers = map[string]platform.Launcher{"sim": f.sim}
	scaler, err := scale.NewScaler(f.store, f.tokens, opts)
	if err != nil {
		t.Fatal(err)
	}
	f.scaler = scaler
}

func (f *fixture) enqueue(t *testing.T, tenant core.TenantID, pool, modelID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		f.seq++
		if _, err := f.store.Enqueue(f.ctx, worker.Invocation{
			ClientRef: fmt.Sprintf("q%d", f.seq), Pool: pool, Tenant: tenant, Model: modelID,
			Request: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func desc(id string) worker.Descriptor {
	return worker.Descriptor{Descriptor: model.Descriptor{ID: id, Provider: "worker", Locality: model.LocalityWorker}}
}

func baseTemplate() scale.Template {
	return scale.Template{
		ID: "sdxl-gpu", Pool: "acme-gpu", Descriptors: []worker.Descriptor{desc("sdxl")},
		Capacity: 4, Min: 0, Max: 3, ColdStart: 2 * time.Minute, Launcher: "sim",
	}
}

func TestDemandAsksForEnoughWorkers(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, f *fixture)
		want    int
	}{
		{
			name: "six units of work and capacity four ask for two",
			prepare: func(t *testing.T, f *fixture) {
				f.pool(t, "acme-gpu", "acme")
				f.template(t, baseTemplate())
				f.enqueue(t, "acme", "acme-gpu", "sdxl", 6)
			},
			want: 2,
		},
		{
			name: "present free units cover the queue",
			prepare: func(t *testing.T, f *fixture) {
				f.pool(t, "acme-gpu", "acme")
				tmpl := baseTemplate()
				f.template(t, tmpl)
				f.joinFromTemplate(t, tmpl)
				f.enqueue(t, "acme", "acme-gpu", "sdxl", 3)
			},
			want: 0,
		},
		{
			name: "a template maximum bounds the demand",
			prepare: func(t *testing.T, f *fixture) {
				f.pool(t, "acme-gpu", "acme")
				tmpl := baseTemplate()
				f.template(t, tmpl)
				f.enqueue(t, "acme", "acme-gpu", "sdxl", 40)
			},
			want: 3,
		},
		{
			name: "the deployment cap bounds the demand",
			prepare: func(t *testing.T, f *fixture) {
				f.reconfigure(t, scale.ScalerOptions{DeploymentCap: 2})
				f.pool(t, "acme-gpu", "acme")
				f.template(t, baseTemplate())
				f.enqueue(t, "acme", "acme-gpu", "sdxl", 40)
			},
			want: 2,
		},
		{
			name: "a tenant's backlog counts up to its concurrency limit",
			prepare: func(t *testing.T, f *fixture) {
				f.pool(t, "shared", "")
				tmpl := baseTemplate()
				tmpl.Pool = "shared"
				tmpl.ID = "shared-gpu"
				tmpl.Max = 10
				f.template(t, tmpl)
				if err := f.store.SetTenantSettings(f.ctx, "shared", "acme", worker.TenantSettings{Weight: 1, Concurrency: 4}); err != nil {
					t.Fatal(err)
				}
				f.enqueue(t, "acme", "shared", "sdxl", 40)
			},
			want: 1,
		},
		{
			name: "the minimum is kept without work",
			prepare: func(t *testing.T, f *fixture) {
				f.pool(t, "acme-gpu", "acme")
				tmpl := baseTemplate()
				tmpl.ID = "warm-gpu"
				tmpl.Min = 1
				f.template(t, tmpl)
			},
			want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.prepare(t, f)
			if err := f.scaler.Tick(f.ctx); err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, tmpl := range mustTemplates(t, f) {
				total += f.sim.Asked(tmpl.ID)
			}
			if total != tc.want {
				t.Fatalf("asked for %d workers, want %d", total, tc.want)
			}
		})
	}
}

func mustTemplates(t *testing.T, f *fixture) []scale.Template {
	t.Helper()
	templates, err := f.tokens.ListTemplates(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	return templates
}

func (f *fixture) joinFromTemplate(t *testing.T, tmpl scale.Template) worker.Worker {
	t.Helper()
	launchID, tokens, err := f.tokens.MintLaunch(f.ctx, tmpl, 1, f.clock.Now().Add(tmpl.ColdStart))
	if err != nil {
		t.Fatal(err)
	}
	_ = launchID
	w, err := f.store.Join(f.ctx, worker.JoinRequest{
		PoolToken: tokens[0], ProtocolVersion: worker.ProtocolVersion,
		Descriptors: tmpl.Descriptors, Labels: tmpl.Labels, Capacity: tmpl.Capacity,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.joined = &joinedWorker{worker: w, credential: w.Credential}
	return w
}

func TestPendingLaunchesCoverDemandOnce(t *testing.T) {
	f := newFixture(t)
	f.pool(t, "acme-gpu", "acme")
	f.template(t, baseTemplate())
	f.enqueue(t, "acme", "acme-gpu", "sdxl", 4)
	if err := f.scaler.Tick(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.sim.Asked("sdxl-gpu"); got != 1 {
		t.Fatalf("asked %d, want 1", got)
	}
	f.enqueue(t, "acme", "acme-gpu", "sdxl", 2)
	if err := f.scaler.Tick(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.sim.Asked("sdxl-gpu"); got != 2 {
		t.Fatalf("asked %d in total, want 2", got)
	}
}

func TestJoinTokenIsSingleUseAndBoundToItsTemplate(t *testing.T) {
	f := newFixture(t)
	f.pool(t, "acme-gpu", "acme")
	tmpl := baseTemplate()
	f.template(t, tmpl)
	launchID, tokens, err := f.tokens.MintLaunch(f.ctx, tmpl, 1, f.clock.Now().Add(tmpl.ColdStart))
	if err != nil {
		t.Fatal(err)
	}
	_ = launchID
	join := func(capacity int, units int) error {
		_, err := f.store.Join(f.ctx, worker.JoinRequest{
			PoolToken: tokens[0], ProtocolVersion: worker.ProtocolVersion,
			Descriptors: []worker.Descriptor{{Descriptor: desc("sdxl").Descriptor, Units: units}},
			Capacity:    capacity,
		})
		return err
	}
	if err := join(4, 0); err != nil {
		t.Fatalf("first join: %v", err)
	}
	if err := join(4, 0); core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("second join: %v, want unauthenticated", err)
	}
	// A token whose worker declares a different capacity is refused with
	// conflict, even before any worker of the pool is present.
	launchID2, tokens2, err := f.tokens.MintLaunch(f.ctx, tmpl, 1, f.clock.Now().Add(tmpl.ColdStart))
	if err != nil {
		t.Fatal(err)
	}
	_ = launchID2
	_, err = f.store.Join(f.ctx, worker.JoinRequest{
		PoolToken: tokens2[0], ProtocolVersion: worker.ProtocolVersion,
		Descriptors: []worker.Descriptor{desc("sdxl")}, Capacity: 8,
	})
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("mismatched join: %v, want conflict", err)
	}
}

func TestJoinTokenExpiresAtTheColdStartDeadline(t *testing.T) {
	f := newFixture(t)
	f.pool(t, "acme-gpu", "acme")
	tmpl := baseTemplate()
	f.template(t, tmpl)
	_, tokens, err := f.tokens.MintLaunch(f.ctx, tmpl, 1, f.clock.Now().Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(5 * time.Minute)
	_, err = f.store.Join(f.ctx, worker.JoinRequest{
		PoolToken: tokens[0], ProtocolVersion: worker.ProtocolVersion,
		Descriptors: tmpl.Descriptors, Capacity: tmpl.Capacity,
	})
	if core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("expired join: %v, want unauthenticated", err)
	}
}

func TestThreeFailedLaunchesDegradeAndReportTheError(t *testing.T) {
	f := newFixture(t)
	f.pool(t, "acme-gpu", "acme")
	tmpl := baseTemplate()
	f.template(t, tmpl)
	f.sim.SetLastLaunchError("quota exceeded")
	f.enqueue(t, "acme", "acme-gpu", "sdxl", 1)
	if _, err := f.store.Enqueue(f.ctx, worker.Invocation{
		ClientRef: "run", Pool: "acme-gpu", Tenant: "acme", Model: "sdxl",
		QueueTimeout: 30 * time.Minute, Request: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	// Advance in small steps; each tick expires a deadline and launches again
	// after the back-off, until three launches in a row have failed.
	var (
		state scale.State
		err   error
	)
	for i := 0; i < 120; i++ {
		if err := f.scaler.Tick(f.ctx); err != nil {
			t.Fatal(err)
		}
		state, err = f.scaler.TemplateState(f.ctx, "sdxl-gpu")
		if err != nil {
			t.Fatal(err)
		}
		if state == scale.StateDegraded {
			break
		}
		f.clock.Advance(30 * time.Second)
	}
	if state != scale.StateDegraded {
		t.Fatalf("template is %s, want degraded; asked %d", state, f.sim.Asked("sdxl-gpu"))
	}
	f.clock.Advance(30 * time.Minute)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	inv, err := f.store.Invocation(f.ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if inv.State != "failed" || inv.Code != core.CodeUnavailable || inv.Message != "quota exceeded" {
		t.Fatalf("invocation %s code %s message %q", inv.State, inv.Code, inv.Message)
	}
}

func TestCatalogListsTemplateModelsAsOnDemand(t *testing.T) {
	f := newFixture(t)
	f.pool(t, "acme-gpu", "acme")
	tmpl := baseTemplate()
	f.template(t, tmpl)
	avail, err := f.store.Availability(f.ctx, "acme-gpu", "sdxl")
	if err != nil {
		t.Fatal(err)
	}
	if avail != worker.AvailabilityOnDemand {
		t.Fatalf("availability %s, want on_demand", avail)
	}
	catalog, err := f.store.Catalog(f.ctx, "acme-gpu")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog[0].Descriptor.ID != "sdxl" || catalog[0].ColdStart != 2*time.Minute {
		t.Fatalf("catalog %+v", catalog)
	}
	if f.joinFromTemplate(t, tmpl).ID == "" {
		t.Fatal("worker did not join")
	}
	if avail, err := f.store.Availability(f.ctx, "acme-gpu", "sdxl"); err != nil || avail != worker.AvailabilityOnline {
		t.Fatalf("availability %s, %v, want online", avail, err)
	}
	if err := f.store.Leave(f.ctx, f.joined.worker.ID, f.joined.credential); err != nil {
		t.Fatal(err)
	}
	if avail, err := f.store.Availability(f.ctx, "acme-gpu", "sdxl"); err != nil || avail != worker.AvailabilityOnDemand {
		t.Fatalf("availability %s, %v, want on_demand after the leave", avail, err)
	}
}

func TestExitPolicy(t *testing.T) {
	started := at
	lastWork := at.Add(time.Minute)
	p := scale.ExitPolicy{IdleExit: 5 * time.Minute, MaxLifetime: time.Hour, MaxInvocations: 3}
	if p.Leave(at.Add(6*time.Minute-time.Second), started, lastWork, 0) {
		t.Fatal("left before the idle-exit time")
	}
	if !p.Leave(at.Add(6*time.Minute), started, lastWork, 0) {
		t.Fatal("did not leave at the idle-exit time")
	}
	if !p.Leave(at.Add(2*time.Minute), started, lastWork, 3) {
		t.Fatal("did not leave after its maximum invocations")
	}
	if !p.Leave(at.Add(time.Hour), started, at.Add(59*time.Minute), 0) {
		t.Fatal("did not leave after its maximum lifetime")
	}
}

func TestTemplateValidation(t *testing.T) {
	f := newFixture(t)
	f.pool(t, "acme-gpu", "acme")
	tmpl := baseTemplate()
	tmpl.Max = 0
	if err := f.tokens.SaveTemplate(f.ctx, tmpl); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v, want invalid_argument", err)
	}
	tmpl = baseTemplate()
	tmpl.Pool = "missing"
	if err := f.tokens.SaveTemplate(f.ctx, tmpl); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("got %v, want not_found", err)
	}
}

func TestColdStartMustFitThePoolQueueTimeout(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.CreatePool(f.ctx, worker.PoolSpec{
		ID: "acme-gpu", Tenant: "acme", Lease: 5 * time.Minute, Grace: time.Minute, QueueTimeout: 3 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	tmpl := baseTemplate()
	if err := f.tokens.SaveTemplate(f.ctx, tmpl); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v, want invalid_argument", err)
	}
	tmpl.ColdStart = time.Minute
	if err := f.tokens.SaveTemplate(f.ctx, tmpl); err != nil {
		t.Fatalf("cold start of one minute: %v", err)
	}
}

func TestLaunchCarriesTheExitPolicy(t *testing.T) {
	f := newFixture(t)
	f.pool(t, "acme-gpu", "acme")
	tmpl := baseTemplate()
	tmpl.Min = 1
	tmpl.IdleExit = 5 * time.Minute
	tmpl.MaxLifetime = 15 * time.Minute
	tmpl.MaxInvocations = 10
	f.template(t, tmpl)
	if err := f.scaler.Tick(f.ctx); err != nil {
		t.Fatal(err)
	}
	requests := f.sim.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests %v", requests)
	}
	if got := requests[0].Settings["idle_exit"]; got != "5m0s" {
		t.Fatalf("idle_exit %q", got)
	}
	if got := requests[0].Settings["max_lifetime"]; got != "15m0s" {
		t.Fatalf("max_lifetime %q", got)
	}
	if got := requests[0].Settings["max_invocations"]; got != "10" {
		t.Fatalf("max_invocations %q", got)
	}
}
