package worker

import (
	"context"
	"encoding/json"
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
