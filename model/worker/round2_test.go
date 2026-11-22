package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/platform/credentials"
	psql "github.com/epzero/basamento/platform/sql"
)

// TestTenantPoolAuditUsesTheRealSQLSink proves that a tenant pool can join,
// leave, be removed and spill through the SQL audit store: the producer must
// put the tenant in the context, not only on the event (REQ-CRED-034).
func TestTenantPoolAuditUsesTheRealSQLSink(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	db, err := psql.Open(ctx, "sqlite:///"+t.TempDir()+"/worker.db", credentials.Migrations())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := coretest.NewFakeClock(at)
	audit := credentials.NewSQLAudit(db)
	s, err := Open(ctx, db, Options{Clock: clock, IDs: &coretest.SeqIds{}, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	if err := s.Leave(ctx, w.ID, w.Credential); err != nil {
		t.Fatalf("tenant-pool leave: %v", err)
	}
	if err := s.Remove(ctx, w.ID); err != nil {
		t.Fatalf("tenant-pool remove: %v", err)
	}
	if err := s.SetTenantSettings(ctx, "p", "acme", TenantSettings{
		Weight: 1, SpillOver: true, SpillAfter: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := encodeRequest(model.Request{Model: "m", Op: model.CapChat, ClientRef: "r1", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, Invocation{
		ClientRef: "r1", Pool: "p", Tenant: "acme", Model: "m", Class: ClassNormal,
		Units: 1, Request: raw, QueueTimeout: time.Hour, MaxRuntime: 5 * time.Minute, ReservedMpts: 100,
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "m", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		result:      model.Result{State: model.StateSucceeded, CostMpts: 42},
	}
	if _, err := s.Spill(ctx, remote.Remote("cloud")); err != nil {
		t.Fatalf("tenant-pool spill: %v", err)
	}
	events, err := audit.Read(core.WithTenant(ctx, "acme"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"worker.join": false, "worker.leave": false, "worker.remove": false, "worker.spill": false}
	for _, e := range events {
		if _, ok := want[e.Action]; ok {
			want[e.Action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Fatalf("no %s audit event for the tenant pool; events %+v", action, events)
		}
	}
}

// rotatingTransport rotates the credential on every heartbeat and records the
// credential each lease presents.
type rotatingTransport struct {
	mu        sync.Mutex
	leases    []string
	rotations int
}

func (t *rotatingTransport) Join(context.Context, JoinRequest) (Worker, error) {
	return Worker{ID: "w1", Credential: "c1"}, nil
}

func (t *rotatingTransport) Lease(_ context.Context, req LeaseRequest) ([]Invocation, error) {
	t.mu.Lock()
	t.leases = append(t.leases, req.Credential)
	t.mu.Unlock()
	return nil, nil
}

func (t *rotatingTransport) Heartbeat(context.Context, string, string, int) (HeartbeatAnswer, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rotations++
	return HeartbeatAnswer{Credential: fmt.Sprintf("c%d", t.rotations+1)}, nil
}

func (t *rotatingTransport) Starting(context.Context, string, string, string, int) error { return nil }
func (t *rotatingTransport) Started(context.Context, string, string, string, string, int) error {
	return nil
}
func (t *rotatingTransport) Events(context.Context, string, string, string, int, []Event) error {
	return nil
}
func (t *rotatingTransport) Finish(context.Context, string, string, string, string, json.RawMessage, core.Code, int64, int) error {
	return nil
}
func (t *rotatingTransport) Release(context.Context, string, string, string, bool, time.Time, int) error {
	return nil
}
func (t *rotatingTransport) Resume(context.Context, string, string, string, int) error { return nil }
func (t *rotatingTransport) Leave(context.Context, string, string) error               { return nil }

func (t *rotatingTransport) lastLease() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.leases) == 0 {
		return ""
	}
	return t.leases[len(t.leases)-1]
}

// TestCredentialRotationIsRaceFree runs the heartbeat and lease goroutines
// against one client; -race reports the old unsynchronised write (SEC-03).
func TestCredentialRotationIsRaceFree(t *testing.T) {
	ctx := context.Background()
	tr := &rotatingTransport{}
	c := NewClient(tr, nil)
	if _, err := c.Join(ctx, JoinRequest{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := c.Heartbeat(ctx, 0); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := c.LeaseOnce(ctx, 1, nil, 0, 0); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	// The next call must present the credential the last heartbeat rotated in.
	want := c.Worker().Credential
	if _, err := c.LeaseOnce(ctx, 1, nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := tr.lastLease(); got != want {
		t.Fatalf("lease presented credential %q, want the rotated %q", got, want)
	}
}

// TestSupersededCancelDoesNotOutliveItsRun proves the pending-cancel map does
// not keep an entry for an attempt the invocation already moved past.
func TestSupersededCancelDoesNotOutliveItsRun(t *testing.T) {
	r := &Runner{}
	if !r.Cancel("r1", 1) {
		t.Fatal("Cancel reported no match for an untracked ref")
	}
	r.mu.Lock()
	if got, ok := r.pending["r1"]; !ok || got != 1 {
		r.mu.Unlock()
		t.Fatalf("pending %+v, want a kept cancel for attempt 1", r.pending)
	}
	r.mu.Unlock()
	cancelled := false
	untrack := r.track("r1", 2, func() { cancelled = true })
	r.mu.Lock()
	_, kept := r.pending["r1"]
	r.mu.Unlock()
	untrack()
	if kept {
		t.Fatal("a cancel for superseded attempt 1 outlived attempt 2")
	}
	if cancelled {
		t.Fatal("a cancel for superseded attempt 1 stopped attempt 2")
	}
}

// TestSpillClaimDeadlineUsesMaxRuntime proves the claim is bounded by the run
// the caller declared, not by one worker heartbeat lease (REQ-MODEL-032).
func TestSpillClaimDeadlineUsesMaxRuntime(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 2 * time.Minute})
	raw, err := encodeRequest(model.Request{Model: "m", Op: model.CapChat, ClientRef: "r1", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, Invocation{
		ClientRef: "r1", Pool: pool.ID, Tenant: "acme", Model: "m", Class: ClassNormal,
		Units: 1, Request: raw, QueueTimeout: time.Hour, MaxRuntime: 5 * time.Minute, ReservedMpts: 100,
	}); err != nil {
		t.Fatal(err)
	}
	deadline, owned, err := s.claimSpill(ctx, state(t, s, "r1"), "cloud", 300)
	if err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Fatal("claimSpill did not take the waiting row")
	}
	want := clock.Now().Add(5*time.Minute + 2*time.Minute)
	if !deadline.Equal(want) {
		t.Fatalf("claim deadline %s, want MaxRuntime plus grace %s", deadline, want)
	}
}

// afterReadyClock reads a fixed time but fires every wait immediately, so a
// poll loop reaches its deadline without sleeping.
type afterReadyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *afterReadyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *afterReadyClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *afterReadyClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// TestSpillExpiryCancelsTheRemoteHandle proves a run that outlives its claim
// does not leave the provider running as a paid orphan.
func TestSpillExpiryCancelsTheRemoteHandle(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := &afterReadyClock{now: at}
	s, err := Open(ctx, openTestDB(t), Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: time.Minute})
	if err := s.SetTenantSettings(ctx, pool.ID, "acme", TenantSettings{
		Weight: 1, SpillOver: true, SpillAfter: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := encodeRequest(model.Request{Model: "m", Op: model.CapChat, ClientRef: "r1", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, Invocation{
		ClientRef: "r1", Pool: pool.ID, Tenant: "acme", Model: "m", Class: ClassNormal,
		Units: 1, Request: raw, QueueTimeout: time.Hour, MaxRuntime: time.Minute, ReservedMpts: 100,
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	remote := &scriptedRemote{
		descriptors: []model.Descriptor{{ID: "m", Caps: []model.Capability{model.CapChat}}},
		estimate:    300,
		result:      model.Result{State: model.StateSucceeded},
		resultWait:  make(chan struct{}),
	}
	// Let the claim deadline pass once the remote has started.
	remote.startHook = func() { clock.Advance(time.Hour) }
	if _, err := s.Spill(ctx, remote.Remote("cloud")); err != nil {
		t.Fatal(err)
	}
	if remote.cancels != 1 {
		t.Fatalf("remote Cancel called %d times, want 1 on expiry", remote.cancels)
	}
	if inv := state(t, s, "r1"); inv.State != stateFailed {
		t.Fatalf("state %s after an expired spill, want failed", inv.State)
	}
}
