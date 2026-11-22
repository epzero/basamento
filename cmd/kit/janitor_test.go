package main

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"testing/fstest"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model/worker"
	psql "github.com/epzero/basamento/platform/sql"
)

func TestJanitorSweepsWithoutAWorkerAsking(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	db, err := psql.Open(ctx, "sqlite:///"+dir+"/worker.db", fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := coretest.NewFakeClock(at)
	store, err := worker.Open(ctx, db, worker.Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePool(ctx, worker.PoolSpec{ID: "p", Tenant: "acme", Lease: time.Minute, Grace: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, worker.Invocation{
		ClientRef: "r1", Pool: "p", Tenant: "acme", Model: "m", Class: worker.ClassNormal,
		Units: 1, Request: json.RawMessage(`{}`), QueueTimeout: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)

	sweeps := make(chan time.Time)
	swept := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	loop := &workerLoop{store: store, sweeps: sweeps, swept: swept, stderr: io.Discard}
	go func() {
		defer close(done)
		loop.janitor(ctx, stop)
	}()
	sweeps <- clock.Now()
	<-swept
	close(stop)
	<-done

	inv, err := store.Invocation(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if inv.State != "failed" || inv.Code != core.CodeUnavailable {
		t.Fatalf("invocation %s code %s, want failed unavailable after the janitor swept", inv.State, inv.Code)
	}
}
