package bus

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/natstest"
)

func TestOpenCloudReusesMatchingAssets(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "bus-assets-ok-*")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := natstest.Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx := context.Background()
	st1, err := OpenCloud(ctx, srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st1.Publish(ctx, "run.r1.deltas", []byte("m1")); err != nil {
		t.Fatal(err)
	}
	_ = st1.Close()
	st2, err := OpenCloud(ctx, srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	seq, err := st2.LastSeq(ctx, "run.r1.deltas")
	if err != nil || seq == 0 {
		t.Fatalf("last seq %d err %v", seq, err)
	}
}

func TestOpenCloudRejectsMismatchedStream(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "bus-assets-bad-*")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := natstest.Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	nc, err := nats.Connect(srv.URL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{"other.>"},
	})
	nc.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenCloud(ctx, srv.URL, Options{})
	if err == nil {
		t.Fatal("expected open failure")
	}
	if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("got code %s err %v", core.CodeOf(err), err)
	}
}

func TestOpenCloudRejectsKVBucketTTL(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "bus-kvttl-*")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := natstest.Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	nc, err := nats.Connect(srv.URL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: dataBucket,
		TTL:    time.Hour,
	})
	nc.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenCloud(ctx, srv.URL, Options{})
	if err == nil {
		t.Fatal("expected failure for bucket ttl")
	}
}
