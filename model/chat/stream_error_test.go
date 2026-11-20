package chat

import (
	"context"
	"errors"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/model"
)

type streamAndErrChat struct {
	closed bool
}

func (s *streamAndErrChat) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, nil
}

func (s *streamAndErrChat) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	writer.Close()
	s.closed = true
	return reader, errors.New("open failed")
}

func TestStreamReturnedWithErrorIsClosed(t *testing.T) {
	f := &streamAndErrChat{}
	a := newAdapter(t, f)
	_, err := model.Invoke(testCtx(t), a, model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-err-stream",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !f.closed {
		t.Fatal("stream was not closed when returned with error")
	}
}
