package worker

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	psql "github.com/epzero/basamento/platform/sql"
)

func joinWith(t *testing.T, s *Store, token string, capacity int, descs ...Descriptor) Worker {
	t.Helper()
	w, err := s.Join(context.Background(), JoinRequest{
		PoolToken:       token,
		ProtocolVersion: ProtocolVersion,
		Descriptors:     descs,
		Capacity:        capacity,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func openTestDB(t *testing.T) *psql.Store {
	t.Helper()
	dir := t.TempDir()
	db, err := psql.Open(context.Background(), "sqlite:///"+dir+"/worker.db", fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// scriptedRemote is a remote provider whose start and result the test scripts.
type scriptedRemote struct {
	descriptors []model.Descriptor
	labels      map[string]string
	estimate    int64
	startErr    error
	result      model.Result
	resultErr   error
	startHook   func()
	// resultWait, when set, makes Result wait for Cancel or the context.
	resultWait chan struct{}
	cancelOnce sync.Once
	cancels    int
	accepted   int
	requests   []model.Request
}

func (r *scriptedRemote) Remote(name string) Remote {
	return Remote{Name: name, Invoker: r, Descriptors: r.descriptors, Labels: r.labels, Estimate: r.estimate}
}

func (r *scriptedRemote) Describe(context.Context) ([]model.Descriptor, error) {
	return r.descriptors, nil
}

func (r *scriptedRemote) Start(_ context.Context, req model.Request) (model.Handle, error) {
	r.accepted++
	r.requests = append(r.requests, req)
	if r.startHook != nil {
		r.startHook()
	}
	if r.startErr != nil {
		return model.Handle{}, r.startErr
	}
	return model.Handle{Provider: "remote", ID: req.ClientRef, ClientRef: req.ClientRef}, nil
}

func (r *scriptedRemote) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return nil, nil
}

func (r *scriptedRemote) Result(ctx context.Context, _ model.Handle) (model.Result, error) {
	if r.resultWait == nil {
		return r.result, r.resultErr
	}
	select {
	case <-ctx.Done():
		return model.Result{}, ctx.Err()
	case <-r.resultWait:
		return r.result, r.resultErr
	}
}

func (r *scriptedRemote) Cancel(context.Context, model.Handle) error {
	r.cancels++
	if r.resultWait != nil {
		r.cancelOnce.Do(func() { close(r.resultWait) })
	}
	return nil
}
func (r *scriptedRemote) Lookup(context.Context, string) (*model.Handle, error) {
	return nil, nil
}

func spillPool(t *testing.T, s *Store, clock *coretest.FakeClock) Pool {
	t.Helper()
	pool := mustPool(t, s, PoolSpec{ID: "acme-llm", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	if _, err := s.Join(context.Background(), JoinRequest{
		PoolToken: pool.Token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor("chat", 1, false)}, Capacity: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTenantSettings(context.Background(), "acme-llm", "acme", TenantSettings{
		Weight: 1, SpillOver: true, SpillAfter: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := encodeRequest(model.Request{Model: "chat", Op: model.CapChat, ClientRef: "r1", Params: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(context.Background(), Invocation{
		ClientRef: "r1", Pool: "acme-llm", Tenant: "acme", Model: "chat", Class: ClassNormal,
		Units: 1, Request: raw, QueueTimeout: 10 * time.Minute, ReservedMpts: 100,
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * time.Minute)
	return pool
}

func TestSpillStartsBeforeMarkingAndCarriesTheRemoteResult(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	spillPool(t, s, clock)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		result:      model.Result{State: model.StateSucceeded, Message: json.RawMessage(`{"ok":true}`), CostMpts: 42},
	}
	remote.startHook = func() {
		inv := state(t, s, "r1")
		if inv.State != stateSpilling {
			t.Fatalf("the row is %q before the remote accepted the start, want spilling", inv.State)
		}
		if inv.ReservedMpts != 300 || inv.WorkerID != "cloud" {
			t.Fatalf("the row reserved %d for %q before the start, want 300 for cloud", inv.ReservedMpts, inv.WorkerID)
		}
	}
	spilled, err := s.Spill(ctx, remote.Remote("cloud"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spilled) != 1 || spilled[0].Reserved != 300 {
		t.Fatalf("spilled %v, want one at 300", spilled)
	}
	if remote.accepted != 1 || len(remote.requests) != 1 || remote.requests[0].Op != model.CapChat {
		t.Fatalf("remote accepted %d requests %v, want the whole request", remote.accepted, remote.requests)
	}
	inv := state(t, s, "r1")
	if inv.State != stateRemote || inv.ChargedMpts != 42 || string(inv.Result) != `{"ok":true}` {
		t.Fatalf("invocation %s charged %d result %s, want remote with the remote result", inv.State, inv.ChargedMpts, inv.Result)
	}
}

// TestSpillCompletionBoundsTheChargedCost proves ADR-0012 §9 on the spill
// settlement path: a remote that reports more than the reservation it was
// given or the platform ceiling settles at the ceiling, never at its own
// report, and the row and audit event carry that charge.
func TestSpillCompletionBoundsTheChargedCost(t *testing.T) {
	cases := []struct {
		name                        string
		maxCost, estimate, reported int64
		want                        int64
	}{
		{"reservation", DefaultMaxCostMpts, 300, 9_000_000_000_000_000, 300},
		{"platform ceiling", 200, 300, 9_000_000_000_000_000, 200},
		{"within both", DefaultMaxCostMpts, 300, 42, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
			clock := coretest.NewFakeClock(at)
			audit := &auditRecorder{}
			s, err := Open(ctx, openTestDB(t), Options{
				Clock: clock, IDs: &coretest.SeqIds{}, Audit: audit, MaxCostMpts: tc.maxCost,
			})
			if err != nil {
				t.Fatal(err)
			}
			spillPool(t, s, clock)
			remote := &scriptedRemote{
				descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
				estimate:    tc.estimate,
				result: model.Result{
					State:    model.StateSucceeded,
					Message:  json.RawMessage(`{"ok":true}`),
					CostMpts: tc.reported,
				},
			}
			spilled, err := s.Spill(ctx, remote.Remote("cloud"))
			if err != nil {
				t.Fatal(err)
			}
			if len(spilled) != 1 || spilled[0].Reserved != tc.estimate {
				t.Fatalf("spilled %v, want one at the %d estimate", spilled, tc.estimate)
			}
			inv := state(t, s, "r1")
			if inv.State != stateRemote || inv.ChargedMpts != tc.want || inv.ReservedMpts != tc.estimate {
				t.Fatalf("invocation %s reserved %d charged %d, want remote reserved %d charged %d",
					inv.State, inv.ReservedMpts, inv.ChargedMpts, tc.estimate, tc.want)
			}
			var audited bool
			for _, e := range audit.events {
				if e.Action == "worker.spill" && e.Resource == "acme-llm" && e.Reason == "cloud" {
					audited = true
				}
			}
			if !audited {
				t.Fatalf("audit %v, want a worker.spill event for acme-llm by cloud", audit.events)
			}
		})
	}
}

func TestSpillFailedStartIsFailedNotSucceeded(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	spillPool(t, s, clock)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		startErr:    core.NewError(core.CodeInvalidArgument, "bad request", nil),
	}
	spilled, err := s.Spill(ctx, remote.Remote("cloud"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spilled) != 0 {
		t.Fatalf("spilled %v, want none", spilled)
	}
	inv := state(t, s, "r1")
	if inv.State != stateFailed || inv.Code != core.CodeInvalidArgument || inv.ChargedMpts != 0 {
		t.Fatalf("invocation %s code %s charged %d, want failed invalid_argument at no cost", inv.State, inv.Code, inv.ChargedMpts)
	}
	if n, _ := s.Waiting(ctx, "acme-llm"); n != 0 {
		t.Fatalf("%d still waiting, want none", n)
	}
}

func TestSpillAmbiguousStartSettlesTheReservation(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	spillPool(t, s, clock)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		startErr:    &core.Error{Code: core.CodeTimeout, Retryable: true},
	}
	if _, err := s.Spill(ctx, remote.Remote("cloud")); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateFailed || inv.Code != core.CodeTimeout || inv.ChargedMpts != 300 {
		t.Fatalf("invocation %s code %s charged %d, want failed timeout charged 300", inv.State, inv.Code, inv.ChargedMpts)
	}
}

func TestSpillRemoteFailureCarriesItsCode(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	spillPool(t, s, clock)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		resultErr:   core.NewError(core.CodePermissionDenied, "no", nil),
	}
	if _, err := s.Spill(ctx, remote.Remote("cloud")); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateFailed || inv.Code != core.CodePermissionDenied {
		t.Fatalf("invocation %s code %s, want failed permission_denied", inv.State, inv.Code)
	}
}

// beat heartbeats a worker and keeps the rotated credential, as the client
// loop does.
func beat(t *testing.T, s *Store, w *Worker, free int) HeartbeatAnswer {
	t.Helper()
	answer, err := s.Heartbeat(context.Background(), w.ID, w.Credential, free)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Credential != "" {
		w.Credential = answer.Credential
	}
	return answer
}

// advanceAlive advances the clock while keeping the worker's credential
// renewed, as a live worker's heartbeat loop does.
func advanceAlive(t *testing.T, s *Store, w *Worker, clock *coretest.FakeClock, d time.Duration) {
	t.Helper()
	for d > 0 {
		step := 4 * time.Minute
		if d < step {
			step = d
		}
		beat(t, s, w, 8)
		clock.Advance(step)
		d -= step
	}
}

func TestHeartbeatRecordsUnitsInUseFromFreeUnits(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWith(t, s, pool.Token, 4, descriptor("m", 1, false))
	beat(t, s, &w, 1)
	got, err := s.Worker(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnitsInUse != 3 {
		t.Fatalf("units_used %d, want 3 (capacity 4, free 1)", got.UnitsInUse)
	}
	beat(t, s, &w, 9)
	if got, _ := s.Worker(ctx, w.ID); got.UnitsInUse != 0 {
		t.Fatalf("units_used %d, want 0 when free exceeds capacity", got.UnitsInUse)
	}
}

func TestLeasePrefersWarmModelOnTie(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWith(t, s, pool.Token, 2, descriptor("m1", 1, false), descriptor("m2", 1, false))
	enqueue(t, s, "a", "p", "acme", "m1", ClassNormal, 0)
	enqueue(t, s, "b", "p", "acme", "m2", ClassNormal, 0)
	got, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 1, WarmModels: []string{"m2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ClientRef != "b" {
		t.Fatalf("leased %v, want the warm m2 invocation", refs(got))
	}
}

// gateClock lets a test decide exactly when a long poll's wait fires.
type gateClock struct {
	mu        sync.Mutex
	now       time.Time
	requested chan struct{}
	fire      chan time.Time
}

func newGateClock(at time.Time) *gateClock {
	return &gateClock{now: at, requested: make(chan struct{}, 1), fire: make(chan time.Time, 1)}
}

func (c *gateClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *gateClock) After(time.Duration) <-chan time.Time {
	c.requested <- struct{}{}
	return c.fire
}

func (c *gateClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestLeaseLongPollReturnsWhenWorkArrives(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := newGateClock(at)
	db := openTestDB(t)
	s, err := Open(ctx, db, Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWith(t, s, pool.Token, 1, descriptor("m", 1, false))
	type outcome struct {
		got []Invocation
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 1, Wait: time.Minute})
		done <- outcome{got, err}
	}()
	<-clock.requested
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	clock.advance(50 * time.Millisecond)
	clock.fire <- clock.Now()
	out := <-done
	if out.err != nil || len(out.got) != 1 || out.got[0].ClientRef != "r1" {
		t.Fatalf("long poll returned %v, %v; want r1", refs(out.got), out.err)
	}
}

func TestLeaseLongPollGivesUpWhenTheWaitElapses(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := newGateClock(at)
	db := openTestDB(t)
	s, err := Open(ctx, db, Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWith(t, s, pool.Token, 1, descriptor("m", 1, false))
	done := make(chan []Invocation, 1)
	go func() {
		got, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 1, Wait: time.Minute})
		if err != nil {
			t.Errorf("lease: %v", err)
		}
		done <- got
	}()
	<-clock.requested
	clock.advance(time.Minute)
	clock.fire <- clock.Now()
	if got := <-done; len(got) != 0 {
		t.Fatalf("leased %v after the wait, want none", refs(got))
	}
}

func TestJoinReportsTheHeartbeatInterval(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWith(t, s, pool.Token, 1, descriptor("m", 1, false))
	if w.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("heartbeat interval %s, want %s", w.HeartbeatInterval, DefaultHeartbeatInterval)
	}
	db := openTestDB(t)
	custom, err := Open(ctx, db, Options{Clock: coretest.NewFakeClock(at), IDs: &coretest.SeqIds{}, HeartbeatInterval: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p2 := mustPool(t, custom, PoolSpec{ID: "p2", Tenant: "acme"})
	if w2 := joinWith(t, custom, p2.Token, 1, descriptor("m", 1, false)); w2.HeartbeatInterval != 5*time.Second {
		t.Fatalf("heartbeat interval %s, want 5s", w2.HeartbeatInterval)
	}
}

func TestReleaseOfAStartedRestartSafeInvocationResetsFollowers(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Started(ctx, w.ID, w.Credential, "r1", "h1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, w.ID, w.Credential, "r1", false, time.Time{}, 1); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateWaiting || inv.Started || inv.ProviderHandle != "" {
		t.Fatalf("invocation %s started %v handle %q, want waiting with no handle", inv.State, inv.Started, inv.ProviderHandle)
	}
	events, err := s.EventsAfter(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "reset" {
		t.Fatalf("events %v, want one reset before the next attempt", events)
	}
}

func TestReleaseUnusedRequeuesANonRestartSafeInvocation(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, w.ID, w.Credential, "r1", true, time.Time{}, 1); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateWaiting || inv.InDoubt || inv.WorkerID != "" {
		t.Fatalf("invocation %s in doubt %v worker %q, want waiting and free", inv.State, inv.InDoubt, inv.WorkerID)
	}
	if got := lease(t, s, w, 1); len(got) != 1 {
		t.Fatalf("leased %v after an unused release, want the invocation", refs(got))
	}
}

func TestRouterHonoursCapability(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "acme-llm", Tenant: "acme"})
	joinWorker(t, s, "acme-llm", pool.Token, "chat", 1, false)
	remote := &scriptedRemote{descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapEmbed}}}}
	r := NewRouter(s, remote.Remote("cloud"))
	dest, err := r.SelectFor(ctx, "acme", "chat", model.CapChat)
	if err != nil {
		t.Fatal(err)
	}
	if dest.IsRemote || dest.Pool != "acme-llm" {
		t.Fatalf("dest %+v, want the pool that supports chat", dest)
	}
	dest, err = r.SelectFor(ctx, "acme", "chat", model.CapEmbed)
	if err != nil {
		t.Fatal(err)
	}
	if !dest.IsRemote {
		t.Fatalf("dest %+v, want the remote that supports embed", dest)
	}
	if _, err := r.SelectFor(ctx, "acme", "chat", model.CapVideoGenerate); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("select for an unsupported capability: got %v, want not_found", err)
	}
}

func TestInvokerStartRejectsAnUnsupportedCapability(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	joinWorker(t, s, "p", pool.Token, "m", 1, false)
	inv := &Invoker{Store: s, Pool: "p"}
	if _, err := inv.Start(ctx, model.Request{Model: "m", Op: model.CapEmbed, ClientRef: "x"}); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("start with an unsupported capability: got %v, want unsupported", err)
	}
	if _, err := s.Invocation(ctx, "x"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("unsupported start queued the invocation: %v", err)
	}
}

func TestRecoverExtendsAndNeverShortens(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "v1", 1); err != nil {
		t.Fatal(err)
	}
	advanceAlive(t, s, &w, clock, 10*time.Minute)
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "v1")
	if want := clock.Now().Add(time.Minute); !inv.LeaseExpires.Equal(want) {
		t.Fatalf("lease expires %s, want the recovered %s", inv.LeaseExpires, want)
	}
	// A deadline already further out is never shortened.
	far := clock.Now().Add(time.Hour)
	if _, err := s.db.Execer().Exec(ctx, `UPDATE worker_invocations SET lease_expires_unix_ms = ? WHERE client_ref = ?`, unixms(far), "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if got := state(t, s, "v1"); !got.LeaseExpires.Equal(far) {
		t.Fatalf("lease expires %s, want the later %s", got.LeaseExpires, far)
	}
	// The in-doubt deadline is extended with the grace period and also never
	// shortened.
	enqueue(t, s, "v2", "p", "acme", "m", ClassNormal, 0)
	beat(t, s, &w, 8)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "v2", 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := state(t, s, "v2"); !got.InDoubt {
		t.Fatalf("v2 is not in doubt: %+v", got)
	}
	clock.Advance(10 * time.Minute)
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	inDoubt := state(t, s, "v2")
	if want := clock.Now().Add(5 * time.Minute); !inDoubt.InDoubtUntil.Equal(want) {
		t.Fatalf("in doubt until %s, want %s", inDoubt.InDoubtUntil, want)
	}
	farDoubt := clock.Now().Add(time.Hour)
	if _, err := s.db.Execer().Exec(ctx, `UPDATE worker_invocations SET in_doubt_until_unix_ms = ? WHERE client_ref = ?`, unixms(farDoubt), "v2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if got := state(t, s, "v2"); !got.InDoubtUntil.Equal(farDoubt) {
		t.Fatalf("in doubt until %s, want the later %s", got.InDoubtUntil, farDoubt)
	}
}

func TestOpenRecoversExpiredLeases(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	clock := coretest.NewFakeClock(at)
	s, err := Open(ctx, db, Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "v1", 1); err != nil {
		t.Fatal(err)
	}
	advanceAlive(t, s, &w, clock, 10*time.Minute)
	reopened, err := Open(ctx, db, Options{Clock: clock, IDs: &coretest.SeqIds{}, Recover: true})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := reopened.Invocation(ctx, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.Now().Add(time.Minute); !inv.LeaseExpires.Equal(want) {
		t.Fatalf("lease expires %s, want the startup recovery to extend it to %s", inv.LeaseExpires, want)
	}
}

func TestJournalDropsTornTrailingLineAndRepairs(t *testing.T) {
	j, err := OpenJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if err := j.Record(JournalEntry{Kind: "lease", ClientRef: "v1", Model: "m", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(JournalEntry{Kind: "lease", ClientRef: "v2", Model: "m", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(j.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"lease","client_ref":"v3"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	entries, err := j.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("read %d entries, want the 2 complete ones", len(entries))
	}
	if err := j.Record(JournalEntry{Kind: "lease", ClientRef: "v3", Model: "m", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	entries, err = j.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[2].ClientRef != "v3" {
		t.Fatalf("read %v after the repair, want v1, v2, v3", entries)
	}
}

func TestJournalRejectsACorruptMiddleLine(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	data := `{"kind":"lease","client_ref":"v1"}` + "\n" + `not json` + "\n" + `{"kind":"lease","client_ref":"v2"}` + "\n"
	if err := os.WriteFile(j.Path(), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Entries(); core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("entries: got %v, want a corruption error", err)
	}
}

func TestSupersededWorkerIsToldToCancel(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	w2 := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w1, 1)
	if err := s.Starting(ctx, w1.ID, w1.Credential, "r1", 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := lease(t, s, w2, 1); len(got) != 1 || got[0].ClientRef != "r1" {
		t.Fatalf("w2 leased %v, want r1", refs(got))
	}
	answer := beat(t, s, &w1, 8)
	found := false
	for _, ref := range answer.Cancel {
		if ref == "r1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("heartbeat answer %v does not tell the superseded worker to cancel", answer.Cancel)
	}
	again := beat(t, s, &w1, 8)
	still := false
	for _, ref := range again.Cancel {
		if ref == "r1" {
			still = true
		}
	}
	if !still {
		t.Fatalf("cancel %v lost before the invocation ended; it must be repeated until acknowledged", again.Cancel)
	}
}

func TestMetricsLeaseAgeStartsAtTheLease(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	advanceAlive(t, s, &w, clock, 10*time.Minute)
	lease(t, s, w, 1)
	clock.Advance(time.Minute)
	m, err := s.Metrics(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if m.MaxLeaseAge != time.Minute {
		t.Fatalf("max lease age %s, want 1m (not the queue age)", m.MaxLeaseAge)
	}
}

func TestSpillClaimsTheRowSoAWorkerCannotTakeIt(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := spillPool(t, s, clock)
	w := joinWith(t, s, pool.Token, 1, descriptor("chat", 1, false))
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		result:      model.Result{State: model.StateSucceeded},
	}
	remote.startHook = func() {
		if got := lease(t, s, w, 1); len(got) != 0 {
			t.Fatalf("a worker leased %v while the row was claimed for a remote", got)
		}
	}
	if _, err := s.Spill(ctx, remote.Remote("cloud")); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "r1"); inv.State != stateRemote {
		t.Fatalf("invocation %s, want remote", inv.State)
	}
}

func TestSpillHonoursRequiredLabels(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	spillPool(t, s, clock)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		result:      model.Result{State: model.StateSucceeded},
	}
	us := remote.Remote("us")
	us.Labels = map[string]string{"region": "us"}
	eu := remote.Remote("eu")
	eu.Labels = map[string]string{"region": "eu"}
	required := WithLabels(ctx, map[string]string{"region": "eu"})
	spilled, err := s.Spill(required, us)
	if err != nil {
		t.Fatal(err)
	}
	if len(spilled) != 0 || remote.accepted != 0 {
		t.Fatalf("spilled %v with %d starts, want none for a remote in another region", spilled, remote.accepted)
	}
	spilled, err = s.Spill(required, us, eu)
	if err != nil {
		t.Fatal(err)
	}
	if len(spilled) != 1 || spilled[0].Remote != "eu" {
		t.Fatalf("spilled %v, want one on eu", spilled)
	}
}

func TestSpillAppliesSharedPoolTenantSettings(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	mustPool(t, s, PoolSpec{ID: "shared", Lease: time.Minute, Grace: time.Minute})
	if err := s.SetTenantSettings(ctx, "shared", "acme", TenantSettings{
		Weight: 1, SpillOver: true, SpillAfter: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, "r1", "shared", "acme", "chat", ClassNormal, 100)
	clock.Advance(3 * time.Minute)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		result:      model.Result{State: model.StateSucceeded},
	}
	spilled, err := s.Spill(ctx, remote.Remote("cloud"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spilled) != 1 {
		t.Fatalf("spilled %v from a shared pool with spill-over on, want one", spilled)
	}
	if inv := state(t, s, "r1"); inv.State != stateRemote {
		t.Fatalf("invocation %s, want remote", inv.State)
	}
}

// cancellingRemote holds its result until the test releases it, and records a
// cancel it receives.
type cancellingRemote struct {
	scriptedRemote
	started   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func (r *cancellingRemote) Start(ctx context.Context, req model.Request) (model.Handle, error) {
	h, err := r.scriptedRemote.Start(ctx, req)
	close(r.started)
	return h, err
}

func (r *cancellingRemote) Result(context.Context, model.Handle) (model.Result, error) {
	<-r.release
	return model.Result{State: model.StateCancelled, CostMpts: 42}, nil
}

func (r *cancellingRemote) Cancel(context.Context, model.Handle) error {
	r.once.Do(func() { close(r.cancelled) })
	return nil
}

func TestCancelReachesASpillingRemoteAndKeepsItsCost(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	spillPool(t, s, clock)
	remote := &cancellingRemote{
		scriptedRemote: scriptedRemote{
			descriptors: []model.Descriptor{{ID: "chat", Caps: []model.Capability{model.CapChat}}},
			estimate:    300,
		},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	rem := Remote{Name: "cloud", Invoker: remote, Descriptors: remote.descriptors, Estimate: 300}
	done := make(chan error, 1)
	go func() {
		_, err := s.Spill(ctx, rem)
		done <- err
	}()
	<-remote.started
	if err := s.Cancel(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	close(remote.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateCancelled || inv.ChargedMpts != 42 {
		t.Fatalf("invocation %s charged %d, want cancelled with the provider's 42", inv.State, inv.ChargedMpts)
	}
}

func TestCrashedWorkerLeavesAvailability(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	if got, err := s.Availability(ctx, "p", "m"); err != nil || got != AvailabilityOnline {
		t.Fatalf("availability %q err %v, want online", got, err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.mustWorker(t, w.ID); got.Present {
		t.Fatalf("a worker that missed its heartbeats is still present")
	}
	if got, err := s.Availability(ctx, "p", "m"); err != nil || got != AvailabilityOffline {
		t.Fatalf("availability %q err %v, want offline after the presence deadline", got, err)
	}
	// The credential outlives presence: the worker's next heartbeat makes it
	// present again without a rejoin.
	beat(t, s, &w, 8)
	if got, err := s.Availability(ctx, "p", "m"); err != nil || got != AvailabilityOnline {
		t.Fatalf("availability %q err %v, want online after a heartbeat", got, err)
	}
	// A draining worker takes no work and is not online.
	if err := s.SetState(ctx, w.ID, StateDraining); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Availability(ctx, "p", "m"); err != nil || got != AvailabilityOffline {
		t.Fatalf("availability %q err %v, want offline for a draining worker", got, err)
	}
}

func (s *Store) mustWorker(t *testing.T, id string) Worker {
	t.Helper()
	w, err := s.Worker(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWorkerCredentialExpiresWithoutAHeartbeat(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	clock.Advance(61 * time.Minute) // past credentialLifetime heartbeat intervals
	if _, err := s.Heartbeat(ctx, w.ID, w.Credential, 1); core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("heartbeat with an expired credential: got %v, want unauthenticated", err)
	}
	if _, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 1}); core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("lease with an expired credential: got %v, want unauthenticated", err)
	}
}

func TestHeartbeatRotatesTheCredential(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	old := w.Credential
	answer := beat(t, s, &w, 1)
	if answer.Credential == "" || answer.Credential == old {
		t.Fatalf("heartbeat credential %q, want a rotation of the join credential", answer.Credential)
	}
	// The rotated credential works, and the lost-response case is covered by
	// the predecessor staying valid until its own expiry.
	if _, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: answer.Credential, FreeUnits: 1}); err != nil {
		t.Fatalf("lease with the rotated credential: %v", err)
	}
	if _, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: old, FreeUnits: 1}); err != nil {
		t.Fatalf("lease with the predecessor credential: %v", err)
	}
}

func TestCredentialSurvivesTwoMissedRotations(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	old := w.Credential
	first := beat(t, s, &w, 1)
	// The worker lost that answer and heartbeats again with its join
	// credential. The rotation must still keep the credential it presents
	// valid; otherwise two lost answers lock a live worker out.
	second, err := s.Heartbeat(ctx, w.ID, old, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.Credential == "" || second.Credential == first.Credential {
		t.Fatalf("second rotation %q, want a fresh credential", second.Credential)
	}
	if _, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: old, FreeUnits: 1}); err != nil {
		t.Fatalf("lease with the credential the worker still holds: %v", err)
	}
	if _, err := s.Heartbeat(ctx, w.ID, second.Credential, 1); err != nil {
		t.Fatalf("heartbeat with the latest credential: %v", err)
	}
}
