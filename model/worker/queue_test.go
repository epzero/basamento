package worker

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	psql "github.com/epzero/basamento/platform/sql"
)

func testStore(t *testing.T, at time.Time) (*Store, *coretest.FakeClock) {
	t.Helper()
	dir := t.TempDir()
	db, err := psql.Open(context.Background(), "sqlite:///"+dir+"/worker.db", fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := coretest.NewFakeClock(at)
	s, err := Open(context.Background(), db, Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	return s, clock
}

func joinWorker(t *testing.T, s *Store, pool, token, modelID string, units int, restartSafe bool) Worker {
	t.Helper()
	w, err := s.Join(context.Background(), JoinRequest{
		PoolToken:       token,
		ProtocolVersion: ProtocolVersion,
		Descriptors:     []Descriptor{descriptor(modelID, units, restartSafe)},
		Capacity:        8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func descriptor(id string, units int, restartSafe bool) Descriptor {
	return Descriptor{
		Descriptor:  model.Descriptor{ID: id, Provider: "test", Caps: []model.Capability{model.CapChat}},
		Units:       units,
		RestartSafe: restartSafe,
	}
}

func TestLeaseOrdersInteractiveBeforeOlderBatch(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool, err := s.CreatePool(ctx, PoolSpec{ID: "p", Tenant: "acme", Lease: 5 * time.Minute, Grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	if _, err := s.Enqueue(ctx, Invocation{ClientRef: "b1", Pool: "p", Tenant: "acme", Model: "m", Class: ClassBatch, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if _, err := s.Enqueue(ctx, Invocation{ClientRef: "i1", Pool: "p", Tenant: "acme", Model: "m", Class: ClassInteractive, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	leased, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || leased[0].ClientRef != "i1" {
		t.Fatalf("leased %v, want i1", refs(leased))
	}
}

func TestLeaseFairnessPrefersTenantWithLessWork(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool, err := s.CreatePool(ctx, PoolSpec{ID: "shared", Lease: 5 * time.Minute, Grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := joinWorker(t, s, "shared", pool.Token, "m", 1, false)
	for _, tenant := range []string{"acme", "globex"} {
		if err := s.SetTenantSettings(ctx, "shared", core.TenantID(tenant), TenantSettings{Weight: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Enqueue(ctx, Invocation{ClientRef: "a" + string(rune('1'+i)), Pool: "shared", Tenant: "acme", Model: "m", Class: ClassNormal, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	leased, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 3 {
		t.Fatalf("leased %v, want the 3 acme invocations", refs(leased))
	}
	if _, err := s.Enqueue(ctx, Invocation{ClientRef: "a4", Pool: "shared", Tenant: "acme", Model: "m", Class: ClassNormal, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if _, err := s.Enqueue(ctx, Invocation{ClientRef: "g1", Pool: "shared", Tenant: "globex", Model: "m", Class: ClassNormal, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	leased, err = s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || leased[0].ClientRef != "g1" {
		t.Fatalf("leased %v, want g1", refs(leased))
	}
}

func refs(invs []Invocation) []string {
	out := make([]string, 0, len(invs))
	for _, inv := range invs {
		out = append(out, inv.ClientRef)
	}
	return out
}

func TestConcurrentLeasesNeverReturnTheSameInvocation(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool, err := s.CreatePool(ctx, PoolSpec{ID: "p", Tenant: "acme", Lease: 5 * time.Minute, Grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w1 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	w2 := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	for i := 0; i < 3; i++ {
		ref := "v" + string(rune('0'+i))
		if _, err := s.Enqueue(ctx, Invocation{ClientRef: ref, Pool: "p", Tenant: "acme", Model: "m", Class: ClassNormal, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	type answer struct {
		got []Invocation
		err error
	}
	start := make(chan struct{})
	answers := make(chan answer, 2)
	var wg sync.WaitGroup
	for _, w := range []Worker{w1, w2} {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := s.Lease(ctx, LeaseRequest{WorkerID: w.ID, Credential: w.Credential, FreeUnits: 3})
			answers <- answer{got, err}
		}()
	}
	close(start)
	wg.Wait()
	close(answers)
	seen := map[string]int{}
	for a := range answers {
		if a.err != nil {
			t.Fatal(a.err)
		}
		for _, inv := range a.got {
			seen[inv.ClientRef]++
		}
	}
	for i := 0; i < 3; i++ {
		ref := "v" + string(rune('0'+i))
		if seen[ref] != 1 {
			t.Fatalf("invocation %s was leased %d times, want once", ref, seen[ref])
		}
	}
}

func TestLeaseRowDoesNotTakeARowAnotherReplicaLeased(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 0)
	// Another replica leases the row after this lease took its candidates.
	if _, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_invocations SET state = ?, worker_id = ? WHERE client_ref = ?`,
		stateLeased, "other", "v1"); err != nil {
		t.Fatal(err)
	}
	// The candidate snapshot this lease read still saw a waiting row.
	candidate := Invocation{ClientRef: "v1", WorkerID: "", State: stateWaiting}
	var taken bool
	if err := s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		var err error
		taken, err = leaseRow(ctx, tx, candidate, w.ID, at, at.Add(pool.Lease))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if taken {
		t.Fatal("leaseRow reported a row another replica already leased")
	}
	inv := state(t, s, "v1")
	if inv.State != stateLeased || inv.WorkerID != "other" {
		t.Fatalf("state %s worker %q, want the other replica's lease", inv.State, inv.WorkerID)
	}
}

func TestLeaseRespectsTenantConcurrencyWithinOneCall(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool, err := s.CreatePool(ctx, PoolSpec{ID: "shared", Lease: 5 * time.Minute, Grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := joinWorker(t, s, "shared", pool.Token, "m", 1, false)
	if err := s.SetTenantSettings(ctx, "shared", "acme", TenantSettings{Weight: 1, Concurrency: 2}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		ref := "a" + string(rune('1'+i))
		if _, err := s.Enqueue(ctx, Invocation{ClientRef: ref, Pool: "shared", Tenant: "acme", Model: "m", Class: ClassNormal, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	leased := lease(t, s, w, 4)
	if len(leased) != 2 {
		t.Fatalf("leased %v, want 2 for a concurrency limit of 2", refs(leased))
	}
}

func TestLeaseSharedPoolInterleavesTenants(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool, err := s.CreatePool(ctx, PoolSpec{ID: "shared", Lease: 5 * time.Minute, Grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := joinWorker(t, s, "shared", pool.Token, "m", 1, false)
	for _, tenant := range []string{"acme", "globex"} {
		if err := s.SetTenantSettings(ctx, "shared", core.TenantID(tenant), TenantSettings{Weight: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range []string{"a1", "a2", "g1", "g2"} {
		tenant := "acme"
		if ref[0] == 'g' {
			tenant = "globex"
		}
		if _, err := s.Enqueue(ctx, Invocation{ClientRef: ref, Pool: "shared", Tenant: core.TenantID(tenant), Model: "m", Class: ClassNormal, Units: 1, Request: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	leased := lease(t, s, w, 4)
	want := []string{"a1", "g1", "a2", "g2"}
	got := refs(leased)
	if len(got) != len(want) {
		t.Fatalf("leased %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leased %v, want %v", got, want)
		}
	}
}

func TestReportsPastGraceAreRefused(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 2 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 500)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "v1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "v1"); !inv.InDoubt {
		t.Fatalf("invocation is not in doubt: %+v", inv)
	}
	clock.Advance(2 * time.Minute)
	for name, err := range map[string]error{
		"starting": s.Starting(ctx, w.ID, w.Credential, "v1"),
		"resume":   s.Resume(ctx, w.ID, w.Credential, "v1"),
		"events":   s.Events(ctx, w.ID, w.Credential, "v1", []Event{{Seq: 1, Kind: "text", Text: "late"}}),
		"release":  s.Release(ctx, w.ID, w.Credential, "v1", time.Time{}),
		"finish":   s.Finish(ctx, w.ID, w.Credential, "v1", stateSucceeded, json.RawMessage(`{}`), "", 0),
	} {
		if core.CodeOf(err) != CodeLeaseLost {
			t.Fatalf("%s past the grace deadline: got %v, want lease_lost", name, err)
		}
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "v1")
	if inv.State != stateFailed || inv.Code != CodeWorkerLost {
		t.Fatalf("state %s code %s, want failed worker_lost", inv.State, inv.Code)
	}
}

func TestStartingRenewsLeaseOfAliveWorker(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "v1", "p", "acme", "m", ClassNormal, 500)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "v1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Starting(ctx, w.ID, w.Credential, "v1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "v1"); inv.State != stateLeased || inv.InDoubt {
		t.Fatalf("state %s in doubt %v after a live report, want leased and not in doubt", inv.State, inv.InDoubt)
	}
	clock.Advance(6 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "v1")
	if inv.State != stateLeased {
		t.Fatalf("state %s code %s, want still leased", inv.State, inv.Code)
	}
	if inv.Code == CodeWorkerLost {
		t.Fatalf("a live worker was failed with worker_lost")
	}
}

func TestConcurrentEnqueueIsIdempotent(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	if _, err := s.CreatePool(ctx, PoolSpec{ID: "p", Tenant: "acme", Lease: 5 * time.Minute, Grace: time.Minute}); err != nil {
		t.Fatal(err)
	}
	const n = 200
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			inv, err := s.Enqueue(ctx, Invocation{ClientRef: "same", Pool: "p", Tenant: "acme", Model: "m", Class: ClassNormal, Units: 1, Request: json.RawMessage(`{}`)})
			if err != nil {
				errs <- err
				return
			}
			if inv.ClientRef != "same" {
				errs <- core.NewError(core.CodeInternal, "enqueue returned another invocation", nil)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent enqueue: %v", err)
	}
	var rows int
	if err := s.db.Execer().QueryRow(ctx, `SELECT COUNT(*) FROM worker_invocations WHERE client_ref = ?`, "same").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d rows for one client ref, want 1", rows)
	}
}

func TestReleaseKeepsOriginalQueueTimeout(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: 5 * time.Minute, Grace: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	if _, err := s.Enqueue(ctx, Invocation{
		ClientRef: "r1", Pool: "p", Tenant: "acme", Model: "m", Class: ClassNormal,
		Units: 1, Request: json.RawMessage(`{}`), QueueTimeout: 10 * time.Minute, ReservedMpts: 500,
	}); err != nil {
		t.Fatal(err)
	}
	lease(t, s, w, 1)
	if err := s.Release(ctx, w.ID, w.Credential, "r1", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(11 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateFailed || inv.Code != core.CodeUnavailable {
		t.Fatalf("state %s code %s, want failed unavailable at the original queue timeout", inv.State, inv.Code)
	}
}

func TestReleaseClampsNotBeforeToLease(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: 5 * time.Minute, Grace: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	// A vendor rate limit beyond one lease period is clamped: a worker must
	// not be able to park work indefinitely.
	if err := s.Release(ctx, w.ID, w.Credential, "r1", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := lease(t, s, w, 1); len(got) != 0 {
		t.Fatalf("leased %v before not_before", refs(got))
	}
	if inv := state(t, s, "r1"); !inv.QueuedAt.Equal(at) || !inv.NotBefore.Equal(at.Add(5*time.Minute)) {
		t.Fatalf("queued at %s not before %s, want %s and %s", inv.QueuedAt, inv.NotBefore, at, at.Add(5*time.Minute))
	}
	clock.Advance(4 * time.Minute)
	if got := lease(t, s, w, 1); len(got) != 0 {
		t.Fatalf("leased %v before the clamped not_before", refs(got))
	}
	clock.Advance(time.Minute)
	got := lease(t, s, w, 1)
	if len(got) != 1 || got[0].ClientRef != "r1" {
		t.Fatalf("leased %v, want r1 once the clamped not_before passed", refs(got))
	}
}

func TestFinishRejectsNegativeCost(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 500)
	lease(t, s, w, 1)
	err := s.Finish(ctx, w.ID, w.Credential, "r1", stateSucceeded, json.RawMessage(`{}`), "", -5)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v, want invalid_argument", err)
	}
	inv := state(t, s, "r1")
	if inv.State != stateLeased || inv.ChargedMpts != 0 {
		t.Fatalf("state %s charged %d, want the lease untouched", inv.State, inv.ChargedMpts)
	}
}
