package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/epzero/basamento/model/worker"
)

// TestOpenWorkerTransportPicksEmbeddedOrDialOut is the E-04 guard: kit worker
// uses the embedded store by default and the WorkerService client when
// --pool-url is set.
func TestOpenWorkerTransportPicksEmbeddedOrDialOut(t *testing.T) {
	ctx := context.Background()

	local, store, closeLocal, err := openWorkerTransport(ctx, "", "sqlite:///"+filepath.Join(t.TempDir(), "worker.db"), worker.TLSConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLocal()
	if _, ok := local.(worker.LocalTransport); !ok {
		t.Fatalf("embedded transport = %T, want worker.LocalTransport", local)
	}
	if store == nil {
		t.Fatal("embedded transport returned no store")
	}

	remote, remoteStore, closeRemote, err := openWorkerTransport(ctx, "127.0.0.1:1", "", worker.TLSConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRemote()
	if _, ok := remote.(*worker.GRPCTransport); !ok {
		t.Fatalf("pool transport = %T, want *worker.GRPCTransport", remote)
	}
	if remoteStore != nil {
		t.Fatalf("pool transport returned store %v, want nil", remoteStore)
	}
}
