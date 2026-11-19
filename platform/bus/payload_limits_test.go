package bus

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/nats-io/nats.go"
)

const (
	stockMaxPayload    = 1 << 20
	stockPublishOK     = 1048505
	stockPublishReject = 1048506
	stockKVOK          = 1048497
	stockKVReject      = 1048498
	embedPublishOK     = 1 << 20
	embedKVOK          = 1048568
)

func TestCASEExpectHeaderConstant71(t *testing.T) {
	const (
		proto  = 10 // NATS/1.0\r\n
		name   = 35 // Nats-Expected-Last-Subject-Sequence
		colon  = 2
		digits = 20
		crlf   = 2
		end    = 2
	)
	if got := proto + name + colon + digits + crlf + end; got != 71 {
		t.Fatalf("derived %d want 71", got)
	}
	if casExpectHeaderMaxBytes != 71 {
		t.Fatalf("constant %d", casExpectHeaderMaxBytes)
	}
}

func TestClusterMaxPayloadFloor(t *testing.T) {
	if !clusterMaxPayloadTooSmall(79) {
		t.Fatal("79 should be too small")
	}
	if clusterMaxPayloadTooSmall(80) {
		t.Fatal("80 should meet floor")
	}
}

func TestOpenRejectsMaxPayload79(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	srv, err := startEmbeddedServer(dir, 79)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownEmbedded(srv)
	nc, err := nats.Connect("", nats.InProcessServer(srv), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_, err = connectAndOpen(ctx, nc, Options{MaxMessageSize: 1}, nil)
	if err == nil || string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("got %v", err)
	}
}

func TestStockOneMiBPayloadBounds(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	srv, err := startEmbeddedServer(dir, stockMaxPayload)
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
	subject := "run.r1.deltas"
	key := "cursor.r1"

	okPub := make([]byte, stockPublishOK)
	if _, err := st.Publish(ctx, subject, okPub); err != nil {
		t.Fatalf("publish ok: %v", err)
	}
	bigPub := make([]byte, stockPublishReject)
	if _, err := st.Publish(ctx, subject, bigPub); err == nil {
		t.Fatal("publish over bound stored")
	} else if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("publish reject code %s", core.CodeOf(err))
	}
	seq, err := st.LastSeq(ctx, subject)
	if err != nil || seq != 1 {
		t.Fatalf("LastSeq %d err %v", seq, err)
	}

	okKV := make([]byte, stockKVOK)
	if _, err := st.Put(ctx, key, okKV, 0); err != nil {
		t.Fatalf("put ok: %v", err)
	}
	bigKV := make([]byte, stockKVReject)
	if _, err := st.Put(ctx, key, bigKV, 0); err == nil {
		t.Fatal("put over bound stored")
	} else if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("put reject code %s", core.CodeOf(err))
	}
	got, _, err := st.Get(ctx, key)
	if err != nil || len(got) != stockKVOK {
		t.Fatalf("get len %d err %v", len(got), err)
	}
	if _, err := st.CompareAndSwap(ctx, key, bigKV, 1, 0); err == nil {
		t.Fatal("swap over bound")
	} else if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("swap reject code %s", core.CodeOf(err))
	}
	if _, err := st.CompareAndSwap(ctx, key, okKV, 1, 0); err != nil {
		t.Fatalf("swap ok: %v", err)
	}
}

func TestOpenEmbeddedPayloadBounds(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := OpenEmbedded(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	subject := "run.r1.embed"
	key := "cursor.r1"
	pub := make([]byte, embedPublishOK)
	if _, err := st.Publish(ctx, subject, pub); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := st.Publish(ctx, subject, make([]byte, embedPublishOK+1)); err == nil {
		t.Fatal("expected publish reject on embedded")
	}
	val := make([]byte, embedKVOK)
	if _, err := st.Put(ctx, key, val, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(ctx, key, make([]byte, embedKVOK+1), 0); err == nil {
		t.Fatal("expected put reject on embedded")
	}
}
