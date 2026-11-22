package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

const shutdownMsg = "temporal: the runtime has shut down"

func assertShutdownUnavailable(t *testing.T, err error) {
	t.Helper()
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeUnavailable || pe.Message != shutdownMsg {
		t.Fatalf("want unavailable %q, got %v", shutdownMsg, err)
	}
}

func TestCloseBeforeDial(t *testing.T) {
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fc.dialCount() != 0 {
		t.Fatalf("dial count %d want 0", fc.dialCount())
	}
	if fc.CloseCount() != 0 {
		t.Fatalf("client Close count %d want 0", fc.CloseCount())
	}
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := rt.Describe(ctx, runtime.RunRef{RunID: "r1"})
	assertShutdownUnavailable(t, err)
	if fc.dialCount() != 0 {
		t.Fatal("dial after close before dial")
	}
}

func TestCloseAfterDialClosesClientOnce(t *testing.T) {
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	_, _ = rt.Describe(ctx, runtime.RunRef{RunID: "r1"})
	if fc.dialCount() != 1 {
		t.Fatalf("dials %d", fc.dialCount())
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("close count %d", fc.CloseCount())
	}
}

func TestCloseIdempotent(t *testing.T) {
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	_, _ = rt.Describe(ctx, runtime.RunRef{RunID: "r1"})
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("close count %d", fc.CloseCount())
	}
}

func TestControlOpsUnavailableAfterClose(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	fc := &fakeClient{
		describeResp: &workflowservice.DescribeWorkflowExecutionResponse{},
		listResp:     &workflowservice.ListWorkflowExecutionsResponse{},
	}
	rt := testControlRuntime(t, fc)
	_, _, _ = rt.List(ctx, runtime.RunQuery{})
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{"Start", func() error {
			_, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r1"})
			return err
		}},
		{"Describe", func() error {
			_, err := rt.Describe(ctx, runtime.RunRef{RunID: "r1"})
			return err
		}},
		{"Cancel", func() error {
			return rt.Cancel(ctx, runtime.RunRef{RunID: "r1"}, "why")
		}},
		{"Resolve", func() error {
			return rt.Resolve(ctx, runtime.RunRef{RunID: "r1"}, "g", nil)
		}},
		{"List", func() error {
			_, _, err := rt.List(ctx, runtime.RunQuery{})
			return err
		}},
	}
	dialsBefore := fc.dialCount()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertShutdownUnavailable(t, tc.run())
		})
	}
	if fc.dialCount() != dialsBefore {
		t.Fatalf("dial count grew to %d from %d", fc.dialCount(), dialsBefore)
	}
}

func TestServeAfterCloseUnavailable(t *testing.T) {
	fc := &fakeClient{}
	rt := testServeRuntime(t, fc, nil)
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	err := rt.Serve(context.Background())
	assertShutdownUnavailable(t, err)
}

type blockingListClient struct {
	*fakeClient
	entered chan struct{}
	release chan struct{}
}

func (b *blockingListClient) ListWorkflow(ctx context.Context, req *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return nil, serviceerror.NewCanceled("grpc: the client connection is closing")
}

func TestCloseConcurrentWithControlRace(t *testing.T) {
	inner := &fakeClient{listResp: &workflowservice.ListWorkflowExecutionsResponse{}}
	bl := &blockingListClient{
		fakeClient: inner,
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(opts client.Options) (client.Client, error) {
		bl.recordDial(opts)
		return bl, nil
	}
	ctx := core.WithTenant(context.Background(), "acme")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, err := rt.List(ctx, runtime.RunQuery{})
		if err == nil {
			t.Error("expected error from in-flight list after close")
			return
		}
		var pe *core.Error
		if !errors.As(err, &pe) || pe.Code != core.CodeCancelled {
			t.Errorf("in-flight list after close: got %v", err)
		}
	}()
	<-bl.entered
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	close(bl.release)
	wg.Wait()
	if inner.CloseCount() != 1 {
		t.Fatalf("close count %d", inner.CloseCount())
	}
}

func TestCloseConcurrentWithControlBarrier(t *testing.T) {
	const workers = 8
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	_, _ = rt.Describe(ctx, runtime.RunRef{RunID: "r1"})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := rt.clientFor(); err != nil {
				assertShutdownUnavailable(t, err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = rt.Close()
	}()
	close(start)
	wg.Wait()
	if fc.CloseCount() != 1 {
		t.Fatalf("close count %d", fc.CloseCount())
	}
}
