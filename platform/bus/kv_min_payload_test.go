package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestKVValueBoundAtMinClusterMaxPayload(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	mp := int32(MinClusterMaxPayloadKV(DefaultMaxMessageSize))
	srv, err := startEmbeddedServer(dir, mp)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownEmbedded(srv)
	nc, err := nats.Connect("", nats.InProcessServer(srv), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	st, err := connectAndOpen(ctx, nc, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	max := MaxKVValueBytes(DefaultMaxMessageSize)
	key := "cursor.r1"
	if _, err := st.Put(ctx, key, make([]byte, max), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompareAndSwap(ctx, key, make([]byte, max), 1, 0); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAcceptsStockOneMiBMaxPayload(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	srv, err := startEmbeddedServer(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownEmbedded(srv)
	nc, err := nats.Connect("", nats.InProcessServer(srv), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if _, err := connectAndOpen(ctx, nc, Options{}, nil); err != nil {
		t.Fatalf("open: %v", err)
	}
}
