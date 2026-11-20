package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/natstest"
)

func TestOpenCloudRejectsIncompatibleStreamRetention(t *testing.T) {
	assertStreamOpenFails(t, jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{busSubjectPrefix + ">"},
		Retention: jetstream.InterestPolicy,
	})
}

func TestOpenCloudRejectsStreamMaxMsgs(t *testing.T) {
	assertStreamOpenFails(t, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{busSubjectPrefix + ">"},
		MaxMsgs:  1,
	})
}

func TestOpenCloudRejectsStreamDenyPurge(t *testing.T) {
	assertStreamOpenFails(t, jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{busSubjectPrefix + ">"},
		DenyPurge: true,
	})
}

func assertStreamOpenFails(t *testing.T, cfg jetstream.StreamConfig) {
	t.Helper()
	srv, err := natstest.Start(t.TempDir())
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
	if _, err := js.CreateStream(context.Background(), cfg); err != nil {
		nc.Close()
		t.Fatal(err)
	}
	nc.Close()
	_, err = OpenCloud(context.Background(), srv.URL, Options{})
	if err == nil {
		t.Fatal("expected failure")
	}
	if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("got %v", err)
	}
}
