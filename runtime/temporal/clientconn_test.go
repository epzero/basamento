package temporal

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
	"go.temporal.io/sdk/client"
)

func TestClientForDialOnceAndShutdown(t *testing.T) {
	fc := &fakeClient{}
	var dials atomic.Int32
	var dialMu sync.Mutex
	started := make(chan struct{})
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(client.Options) (client.Client, error) {
		dialMu.Lock()
		defer dialMu.Unlock()
		if dials.Add(1) == 1 {
			close(started)
		}
		<-started
		return fc, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rt.clientFor(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if dials.Load() != 1 {
		t.Fatalf("dials %d", dials.Load())
	}
	rt.shutdown()
	rt.shutdown()
	if fc.CloseCount() != 1 {
		t.Fatalf("close %d", fc.CloseCount())
	}
	if _, err := rt.clientFor(); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("after shutdown %v", err)
	}
}

func TestFailedDialNotCached(t *testing.T) {
	fc := &fakeClient{}
	attempts := 0
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	rt.dial = func(client.Options) (client.Client, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("dial fail")
		}
		return fc, nil
	}
	if _, err := rt.clientFor(); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("first %v", err)
	}
	if _, err := rt.clientFor(); err != nil {
		t.Fatalf("second %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts %d", attempts)
	}
}

func TestDialOptsCarryConverterAndPropagator(t *testing.T) {
	rt, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
		Client:   client.Options{HostPort: "h:7233", Namespace: "ns1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{}
	rt.dial = func(opts client.Options) (client.Client, error) {
		fc.mu.Lock()
		fc.calls = append(fc.calls, fakeClientCall{Op: "dial", DialOpts: opts})
		fc.mu.Unlock()
		return fc, nil
	}
	if _, err := rt.clientFor(); err != nil {
		t.Fatal(err)
	}
	seen := fc.Calls()[0].DialOpts
	if seen.DataConverter != rt.converter {
		t.Fatal("converter identity")
	}
	if seen.HostPort != "h:7233" || seen.Namespace != "ns1" {
		t.Fatalf("host/ns %#v", seen)
	}
	if len(seen.ContextPropagators) != 1 {
		t.Fatalf("propagators %d", len(seen.ContextPropagators))
	}
	if _, ok := seen.ContextPropagators[0].(TenantPropagator); !ok {
		t.Fatalf("propagator type %T", seen.ContextPropagators[0])
	}
}
