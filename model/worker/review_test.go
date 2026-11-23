package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func TestLeaseReportsCarryTheirAttempt(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)

	for name, err := range map[string]error{
		"starting": s.Starting(ctx, w.ID, w.Credential, "r1", 2),
		"started":  s.Started(ctx, w.ID, w.Credential, "r1", "h1", 2),
		"events":   s.Events(ctx, w.ID, w.Credential, "r1", 2, []Event{{Seq: 1, Kind: "text", Text: "x"}}),
		"release":  s.Release(ctx, w.ID, w.Credential, "r1", true, time.Time{}, 2),
		"finish":   s.Finish(ctx, w.ID, w.Credential, "r1", stateSucceeded, json.RawMessage(`{}`), "", 0, 2),
	} {
		if core.CodeOf(err) != CodeLeaseLost {
			t.Fatalf("%s from another attempt: got %v, want lease_lost", name, err)
		}
	}
	if inv := state(t, s, "r1"); inv.State != stateLeased || inv.Attempt != 1 {
		t.Fatalf("state %s attempt %d after stale reports, want leased attempt 1", inv.State, inv.Attempt)
	}
	if err := s.Starting(ctx, w.ID, w.Credential, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(ctx, w.ID, w.Credential, "r1", 2); core.CodeOf(err) != CodeLeaseLost {
		t.Fatalf("resume from another attempt: got %v, want lease_lost", err)
	}
	if err := s.Finish(ctx, w.ID, w.Credential, "r1", stateSucceeded, json.RawMessage(`{}`), "", 0, 1); err != nil {
		t.Fatal(err)
	}
}

func TestCancelOfWaitingInvocationEndsIt(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	if err := s.Cancel(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "r1"); inv.State != stateCancelled {
		t.Fatalf("state %s, want cancelled", inv.State)
	}
	if got := lease(t, s, w, 1); len(got) != 0 {
		t.Fatalf("leased %v after a cancel", refs(got))
	}
}

func TestCancelledLeaseIsNeverRequeued(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, w.ID, w.Credential, "r1", false, time.Time{}, 1); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "r1"); inv.State != stateCancelled || inv.ChargedMpts != 0 {
		t.Fatalf("state %s charged %d, want cancelled and free", inv.State, inv.ChargedMpts)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "r1"); inv.State != stateCancelled {
		t.Fatalf("state %s after a sweep, want cancelled", inv.State)
	}
}

func TestSweepCancelsRedeliveryOfCancelledWork(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, true)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "r1"); inv.State != stateCancelled || inv.ChargedMpts != 0 {
		t.Fatalf("state %s charged %d after redelivery, want cancelled free", inv.State, inv.ChargedMpts)
	}
}

func TestWorkerReportedCostIsBounded(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	if _, err := s.Enqueue(ctx, Invocation{
		ClientRef: "r1", Pool: "p", Tenant: "acme", Model: "m", Class: ClassNormal,
		Units: 1, Request: json.RawMessage(`{}`), QueueTimeout: 10 * time.Minute, ReservedMpts: 500,
	}); err != nil {
		t.Fatal(err)
	}
	lease(t, s, w, 1)
	if err := s.Finish(ctx, w.ID, w.Credential, "r1", stateSucceeded, json.RawMessage(`{}`), "", 9000000000000000, 1); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "r1"); inv.ChargedMpts != 500 {
		t.Fatalf("charged %d, want the 500 reservation as the bound", inv.ChargedMpts)
	}
}

func TestWorkerEventsAreBounded(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	many := make([]Event, MaxEventsPerBatch+1)
	if err := s.Events(ctx, w.ID, w.Credential, "r1", 1, many); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("too many events: got %v, want invalid_argument", err)
	}
	big := []Event{{Seq: 1, Kind: "audio", Audio: make([]byte, MaxEventBytes+1)}}
	if err := s.Events(ctx, w.ID, w.Credential, "r1", 1, big); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("oversize event: got %v, want invalid_argument", err)
	}
	ok := []Event{{Seq: 1, Kind: "text", Text: "a"}, {Seq: 2, Kind: "text", Text: "b"}}
	if err := s.Events(ctx, w.ID, w.Credential, "r1", 1, ok); err != nil {
		t.Fatal(err)
	}
}

func TestEndedInvocationsAreForgotten(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, clock := testStore(t, at)
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: 5 * time.Minute})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Events(ctx, w.ID, w.Credential, "r1", 1, []Event{{Seq: 1, Kind: "text", Text: "secret"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, w.ID, w.Credential, "r1", stateSucceeded, json.RawMessage(`{}`), "", 0, 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Hour)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Invocation(ctx, "r1"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("ended invocation still held: %v", err)
	}
	if events, err := s.EventsAfter(ctx, "r1", 0); err != nil || len(events) != 0 {
		t.Fatalf("events after eviction: %v %v", events, err)
	}
	if _, err := s.Enqueue(ctx, Invocation{
		ClientRef: "r1", Pool: "p", Tenant: "acme", Model: "m", Class: ClassNormal,
		Units: 1, Request: json.RawMessage(`{}`), QueueTimeout: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if inv := state(t, s, "r1"); inv.State != stateWaiting {
		t.Fatalf("re-enqueue after eviction state %s, want a new waiting invocation", inv.State)
	}
}

func TestJournalPendingClearsADoneOnReLease(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	for _, e := range []JournalEntry{
		{Kind: "lease", ClientRef: "r1", Attempt: 1},
		{Kind: "done", ClientRef: "r1", Attempt: 1},
		{Kind: "lease", ClientRef: "r1", Attempt: 2},
	} {
		if err := j.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ClientRef != "r1" || pending[0].Attempt != 2 {
		t.Fatalf("pending %+v, want the re-leased attempt 2", pending)
	}
}

func TestJournalCompactsDeliveredEntriesOnOpen(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []JournalEntry{
		{Kind: "lease", ClientRef: "done1", Attempt: 1, Request: json.RawMessage(`{"prompt":"secret"}`)},
		{Kind: "handle", ClientRef: "done1", ProviderHandle: "h1"},
		{Kind: "done", ClientRef: "done1"},
		{Kind: "lease", ClientRef: "live", Attempt: 1},
		{Kind: "handle", ClientRef: "live", ProviderHandle: "h2"},
	} {
		if err := j.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	entries, err := reopened.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ClientRef != "live" || entries[0].ProviderHandle != "h2" {
		t.Fatalf("compacted entries %+v, want only the live lease with its handle", entries)
	}
}
