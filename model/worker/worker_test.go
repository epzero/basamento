package worker

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/platform"
)

func mustPool(t *testing.T, s *Store, spec PoolSpec) Pool {
	t.Helper()
	if spec.Lease == 0 {
		spec.Lease = 5 * time.Minute
	}
	if spec.Grace == 0 {
		spec.Grace = time.Minute
	}
	p, err := s.CreatePool(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func enqueue(t *testing.T, s *Store, ref, pool, tenant, modelID string, class Class, reserved int64) {
	t.Helper()
	if _, err := s.Enqueue(context.Background(), Invocation{
		ClientRef: ref, Pool: pool, Tenant: core.TenantID(tenant), Model: modelID,
		Class: class, Units: 1, Request: json.RawMessage(`{}`), ReservedMpts: reserved,
	}); err != nil {
		t.Fatal(err)
	}
}

func lease(t *testing.T, s *Store, w Worker, free int) []Invocation {
	t.Helper()
	got, err := s.Lease(context.Background(), LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: free})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func state(t *testing.T, s *Store, ref string) Invocation {
	t.Helper()
	inv, err := s.Invocation(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestQueueTimeoutFailsUnavailableAndReleasesReservation(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	if _, err := s.Enqueue(ctx, Invocation{
		ClientRef: "r1", Pool: "p", Tenant: "acme", Model: "m", Class: ClassNormal,
		Units: 1, Request: json.RawMessage(`{}`), QueueTimeout: 10 * time.Minute, ReservedMpts: 500,
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(11 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateFailed || inv.Code != core.CodeUnavailable {
		t.Fatalf("state %s code %s, want failed unavailable", inv.State, inv.Code)
	}
	if inv.ChargedMpts != 0 {
		t.Fatalf("charged %d, want the reservation released", inv.ChargedMpts)
	}
}

func TestNotStartedLeaseIsRedeliveredWithSameClientRef(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	w2 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 0)
	leased := lease(t, s, w1, 1)
	if len(leased) != 1 || leased[0].ClientRef != "v1" {
		t.Fatalf("leased %v", refs(leased))
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	leased = lease(t, s, w2, 1)
	if len(leased) != 1 || leased[0].ClientRef != "v1" {
		t.Fatalf("second worker leased %v, want v1", refs(leased))
	}
	if leased[0].Attempt != 2 {
		t.Fatalf("attempt %d, want 2", leased[0].Attempt)
	}
}

func TestRestartSafeExpiryAddsResetEvent(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	enqueue(t, s, "t1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w1, 1)
	if err := s.Starting(ctx, w1.ID, w1.Credential, "t1", 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsAfter(ctx, "t1", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "reset" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events %v, want a reset", events)
	}
}

func TestUnsafeStartedLeaseStaysInDoubtThenFailsWorkerLost(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 2 * time.Minute})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 500)
	lease(t, s, w1, 1)
	if err := s.Starting(ctx, w1.ID, w1.Credential, "v1", 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "v1"); !inv.InDoubt || inv.State != stateLeased {
		t.Fatalf("state %s in doubt %v, want leased in doubt", inv.State, inv.InDoubt)
	}
	clock.Advance(3 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "v1")
	if inv.State != stateFailed || inv.Code != CodeWorkerLost {
		t.Fatalf("state %s code %s, want failed worker_lost", inv.State, inv.Code)
	}
	if inv.ChargedMpts != 500 {
		t.Fatalf("charged %d, want the 500 reservation spent", inv.ChargedMpts)
	}
}

func TestResumeInsideGraceFinishesSuccessfully(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w1, 1)
	if err := s.Starting(ctx, w1.ID, w1.Credential, "v1", 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(ctx, w1.ID, w1.Credential, "v1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, w1.ID, w1.Credential, "v1", stateSucceeded, json.RawMessage(`{}`), "", 0, 1); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "v1"); inv.State != stateSucceeded {
		t.Fatalf("state %s, want succeeded", inv.State)
	}
}

func TestThirdExpiryFailsWorkerLost(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	w2 := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	w3 := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	enqueue(t, s, "x", "p", "acme", "m", ClassNormal, 0)
	for i, w := range []Worker{w1, w2, w3} {
		if got := lease(t, s, w, 1); len(got) != 1 {
			t.Fatalf("lease %d got %v", i+1, refs(got))
		}
		if err := s.Starting(ctx, w.ID, w.Credential, "x", i+1); err != nil {
			t.Fatal(err)
		}
		clock.Advance(2 * time.Minute)
		if err := s.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	inv := state(t, s, "x")
	if inv.State != stateFailed || inv.Code != CodeWorkerLost {
		t.Fatalf("state %s code %s, want failed worker_lost", inv.State, inv.Code)
	}
}

func TestRecoverExtendsLeasesFromRestart(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: 2 * time.Minute})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w1, 1)
	if err := s.Starting(ctx, w1.ID, w1.Credential, "v1", 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(10 * time.Minute)
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "v1")
	if inv.State != stateLeased || inv.WorkerID != w1.ID {
		t.Fatalf("state %s worker %s, want still leased to %s", inv.State, inv.WorkerID, w1.ID)
	}
}

func TestHeartbeatCarriesCancellation(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w1, 1)
	if err := s.Cancel(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	answer := beat(t, s, &w1, 1)
	if len(answer.Cancel) != 1 || answer.Cancel[0] != "r1" {
		t.Fatalf("cancel %v, want r1", answer.Cancel)
	}
}

func TestJoinConflictAndRevokedToken(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	audit := &auditRecorder{}
	s.audit = audit
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	joinWorker(t, s, "p", pool.Token, "m", 1, false)
	if _, err := s.Join(ctx, JoinRequest{PoolToken: pool.Token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor("m", 2, false)}, Capacity: 4}); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("got %v, want conflict", err)
	}
	if err := s.RevokeToken(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(ctx, JoinRequest{PoolToken: pool.Token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor("m", 1, false)}, Capacity: 4}); core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
	found := false
	for _, e := range audit.events {
		if e.Action == "worker.refuse" && e.Resource == "p" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit %v, want a refused join to p", audit.events)
	}
}

func TestRemovedWorkerIsUnauthenticated(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	if err := s.Remove(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 4})
	if core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
}

func TestJournalReplaysUndeliveredLeases(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Record(JournalEntry{Kind: "lease", ClientRef: "v1", Model: "m", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(JournalEntry{Kind: "handle", ClientRef: "v1", ProviderHandle: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(JournalEntry{Kind: "lease", ClientRef: "v2", Model: "m", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(JournalEntry{Kind: "done", ClientRef: "v2"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ClientRef != "v1" || pending[0].ProviderHandle != "h1" {
		t.Fatalf("pending %v, want v1 with its handle", pending)
	}
}

func TestSpillOverReservesBeforeStarting(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "acme-llm", Tenant: "acme"})
	declare(t, s, "acme-llm", pool.Token, "chat")
	if err := s.SetTenantSettings(ctx, "acme-llm", "acme", TenantSettings{Weight: 1, SpillOver: true, SpillAfter: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, "r1", "acme-llm", "acme", "chat", ClassNormal, 0)
	remote := &fakeRemote{estimate: 300}
	clock.Advance(3 * time.Minute)
	spilled, err := s.Spill(ctx, remote.Remote("cloud"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spilled) != 1 || spilled[0].Reserved != 300 {
		t.Fatalf("spilled %v, want one at 300", spilled)
	}
	if remote.accepted != 1 || remote.refs[0] != "r1" {
		t.Fatalf("remote accepted %v, want r1", remote.refs)
	}
	if n, _ := s.Waiting(ctx, "acme-llm"); n != 0 {
		t.Fatalf("%d still waiting, want none", n)
	}
}

func TestSpillOverOffKeepsWorkQueued(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	mustPool(t, s, PoolSpec{ID: "acme-llm", Tenant: "acme"})
	enqueue(t, s, "r1", "acme-llm", "acme", "chat", ClassNormal, 0)
	remote := &fakeRemote{estimate: 300}
	clock.Advance(11 * time.Minute)
	spilled, err := s.Spill(ctx, remote.Remote("cloud"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spilled) != 0 || remote.accepted != 0 {
		t.Fatalf("spilled %v accepted %d, want none", spilled, remote.accepted)
	}
}

func TestRouterPrefersTenantPoolAndLabels(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "acme-llm", Tenant: "acme"})
	if _, err := s.Join(ctx, JoinRequest{PoolToken: pool.Token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor("chat", 1, false)}, Labels: map[string]string{"region": "eu"}, Capacity: 4}); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{labels: map[string]string{"region": "us"}}
	r := NewRouter(s, remote.Remote("cloud"))
	dest, err := r.Select(WithLabels(ctx, map[string]string{"region": "eu"}), "acme", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if dest.IsRemote || dest.Pool != "acme-llm" {
		t.Fatalf("dest %+v, want the tenant pool", dest)
	}
	dest, err = r.Select(WithLabels(ctx, map[string]string{"region": "us"}), "acme", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if !dest.IsRemote {
		t.Fatalf("dest %+v, want the remote provider when no worker matches the labels", dest)
	}
	// A remote that does not carry the required label is not a match either.
	if _, err := r.Select(WithLabels(ctx, map[string]string{"region": "ap"}), "acme", "chat"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("select with an unmatched remote label: got %v, want not_found", err)
	}
}

func TestMetricsReportsQueueAndWorkers(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "a", "p", "acme", "m", ClassInteractive, 0)
	enqueue(t, s, "b", "p", "acme", "m", ClassBatch, 0)
	clock.Advance(time.Minute)
	lease(t, s, w, 1)
	m, err := s.Metrics(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if m.Waiting != 1 || m.Leased != 1 || m.OldestWait != time.Minute {
		t.Fatalf("metrics %+v, want 1 waiting (1m) and 1 leased", m)
	}
	if m.Workers[StateActive] != 1 {
		t.Fatalf("workers %v, want one active", m.Workers)
	}
}

// declare gives the pool a descriptor with no present worker.
func declare(t *testing.T, s *Store, poolID, token, modelID string) {
	t.Helper()
	w, err := s.Join(context.Background(), JoinRequest{PoolToken: token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor(modelID, 1, false)}, Capacity: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Leave(context.Background(), w.ID, w.Credential); err != nil {
		t.Fatal(err)
	}
}

type auditRecorder struct{ events []platform.AuditEvent }

func (a *auditRecorder) Record(_ context.Context, e platform.AuditEvent) error {
	a.events = append(a.events, e)
	return nil
}

type fakeRemote struct {
	estimate int64
	labels   map[string]string
	accepted int
	refs     []string
}

func (f *fakeRemote) Remote(name string) Remote {
	return Remote{
		Name:        name,
		Invoker:     f,
		Descriptors: []model.Descriptor{{ID: "chat"}, {ID: "m"}},
		Labels:      f.labels,
		Estimate:    f.estimate,
	}
}

func (f *fakeRemote) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{{ID: "chat"}, {ID: "m"}}, nil
}

func (f *fakeRemote) Start(_ context.Context, r model.Request) (model.Handle, error) {
	f.accepted++
	f.refs = append(f.refs, r.ClientRef)
	return model.Handle{Provider: "cloud", ID: r.ClientRef, ClientRef: r.ClientRef}, nil
}

func (f *fakeRemote) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return nil, nil
}

func (f *fakeRemote) Result(context.Context, model.Handle) (model.Result, error) {
	return model.Result{State: model.StateSucceeded}, nil
}

func (f *fakeRemote) Cancel(context.Context, model.Handle) error { return nil }

func (f *fakeRemote) Lookup(context.Context, string) (*model.Handle, error) { return nil, nil }

func TestConcurrentJoinsWithConflictingDescriptorsLeaveOneWorker(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	const n = 6
	type result struct {
		w   Worker
		err error
	}
	start := make(chan struct{})
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		units := i + 1
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w, err := s.Join(ctx, JoinRequest{
				PoolToken:       pool.Token,
				ProtocolVersion: ProtocolVersion,
				Descriptors:     []Descriptor{descriptor("m", units, false)},
				Capacity:        8,
			})
			results <- result{w, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var winners []Worker
	conflicts := 0
	for r := range results {
		switch {
		case r.err == nil:
			winners = append(winners, r.w)
		case core.CodeOf(r.err) == core.CodeConflict:
			conflicts++
		default:
			t.Fatalf("join: %v", r.err)
		}
	}
	if len(winners) != 1 || conflicts != n-1 {
		t.Fatalf("%d joins won and %d conflicted, want 1 and %d", len(winners), conflicts, n-1)
	}
	workers, err := s.ListWorkers(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || !workers[0].Present {
		t.Fatalf("pool has %d workers, want one present winner", len(workers))
	}
	descriptors, err := s.Descriptors(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].Units != winners[0].Descriptors[0].Units {
		t.Fatalf("pool descriptor %+v, want the winner's %+v", descriptors, winners[0].Descriptors)
	}
}
