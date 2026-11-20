package chat

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

type infiniteStreamChat struct {
	mu       sync.Mutex
	closed   bool
	empty    bool
	producer chan struct{}
}

func (f *infiniteStreamChat) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, nil
}

func (f *infiniteStreamChat) Stream(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](8)
	f.mu.Lock()
	f.producer = make(chan struct{})
	prodDone := f.producer
	f.mu.Unlock()
	go func() {
		defer writer.Close()
		defer close(prodDone)
		for {
			select {
			case <-ctx.Done():
				f.mu.Lock()
				f.closed = true
				f.mu.Unlock()
				return
			default:
			}
			var msg *schema.Message
			if f.empty {
				msg = &schema.Message{Role: schema.Assistant}
			} else {
				msg = &schema.Message{Role: schema.Assistant, Content: "x"}
			}
			if closed := writer.Send(msg, nil); closed {
				f.mu.Lock()
				f.closed = true
				f.mu.Unlock()
				return
			}
		}
	}()
	return reader, nil
}

func (f *infiniteStreamChat) streamClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *infiniteStreamChat) waitProducer(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	ch := f.producer
	f.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-t.Context().Done():
		t.Fatal("timed out waiting for stream producer")
	}
}

func adapterWithLimits(t *testing.T, m einomodel.BaseChatModel, maxBytes, maxChunks int) *Adapter {
	t.Helper()
	a := newAdapter(t, m)
	a.maxStreamPayloadBytes = maxBytes
	a.maxStreamChunks = maxChunks
	return a
}

func TestStreamPayloadLimitEndsInvocation(t *testing.T) {
	f := &infiniteStreamChat{}
	a := adapterWithLimits(t, f, 16, defaultMaxStreamChunks)
	before := runtime.NumGoroutine()
	req := model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-bytes",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	}
	res, err := model.Invoke(testCtx(t), a, req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.State != model.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	var failure model.Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failure.Code != core.CodeVendor {
		t.Fatalf("code = %q, want vendor_error", failure.Code)
	}
	want := "stream payload exceeded 16 bytes"
	if failure.Message != want {
		t.Fatalf("message = %q, want %q", failure.Message, want)
	}
	f.waitProducer(t)
	if !f.streamClosed() {
		t.Fatal("vendor stream was not closed")
	}
	if runtime.NumGoroutine() > before+2 {
		t.Fatalf("goroutines before=%d after=%d", before, runtime.NumGoroutine())
	}
}

func TestStreamChunkLimitEndsInvocation(t *testing.T) {
	f := &infiniteStreamChat{empty: true}
	a := adapterWithLimits(t, f, defaultMaxStreamPayloadBytes, 4)
	before := runtime.NumGoroutine()
	res, err := model.Invoke(testCtx(t), a, model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-chunks",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.State != model.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	var failure model.Failure
	_ = json.Unmarshal(res.Error, &failure)
	want := "stream chunk count exceeded 4"
	if failure.Message != want {
		t.Fatalf("message = %q, want %q", failure.Message, want)
	}
	f.waitProducer(t)
	if !f.streamClosed() {
		t.Fatal("vendor stream was not closed")
	}
	if runtime.NumGoroutine() > before+2 {
		t.Fatalf("goroutines before=%d after=%d", before, runtime.NumGoroutine())
	}
}
