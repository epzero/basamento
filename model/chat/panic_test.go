package chat

import (
	"context"
	"encoding/json"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

type panicChat struct {
	when string
}

func (p *panicChat) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, nil
}

func (p *panicChat) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if p.when == "stream" {
		panic("vendor exploded: secret-token")
	}
	src := schema.StreamReaderFromArray([]*schema.Message{{Content: "a"}, {Content: "b"}})
	return schema.StreamReaderWithConvert(src, func(m *schema.Message) (*schema.Message, error) {
		if m.Content == "b" {
			panic("recv panic: secret-token")
		}
		return m, nil
	}), nil
}

func assertPanicFailed(t *testing.T, res model.Result) {
	t.Helper()
	if res.State != model.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	var failure model.Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failure.Code != core.CodeInternal || failure.Message != "vendor call panicked" {
		t.Fatalf("failure = %+v", failure)
	}
	if failure.Message != "vendor call panicked" {
		t.Fatalf("stored message must not carry panic text: %q", failure.Message)
	}
}

func TestVendorPanicInStreamSurvives(t *testing.T) {
	a := newAdapter(t, &panicChat{when: "stream"})
	res, err := model.Invoke(testCtx(t), a, model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-panic-stream",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	assertPanicFailed(t, res)
	ok, err := model.Invoke(testCtx(t), a, model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-after",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("second invoke err: %v", err)
	}
	_ = ok
}

func TestVendorPanicInRecvSurvives(t *testing.T) {
	a := newAdapter(t, &panicChat{when: "recv"})
	res, err := model.Invoke(testCtx(t), a, model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-panic-recv",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	assertPanicFailed(t, res)
}
