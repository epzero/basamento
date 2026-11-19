package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

func TestContentlessStreamFails(t *testing.T) {
	const vendorOnly = "sensitive vendor refusal text"
	cases := []struct {
		name   string
		chunks []*schema.Message
	}{
		{name: "no chunks", chunks: nil},
		{name: "role only", chunks: []*schema.Message{{Role: schema.Assistant}}},
		{name: "empty content", chunks: []*schema.Message{{Role: schema.Assistant, Content: ""}}},
		{
			name: "extra only",
			chunks: []*schema.Message{{
				Role:  schema.Assistant,
				Extra: map[string]any{"refusal": vendorOnly},
			}},
		},
		{
			name: "response meta only",
			chunks: []*schema.Message{{
				Role:         schema.Assistant,
				ResponseMeta: &schema.ResponseMeta{FinishReason: "content_filter"},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdapter(t, &fakeChat{chunks: tc.chunks})
			res, err := model.Invoke(testCtx(t), a, model.Request{
				Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-" + tc.name,
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
			if failure.Code != core.CodeVendor {
				t.Fatalf("code = %q", failure.Code)
			}
			want := "chat stream produced no usable output"
			if failure.Message != want {
				t.Fatalf("message = %q, want %q", failure.Message, want)
			}
			if strings.Contains(failure.Message, vendorOnly) {
				t.Fatalf("error echoed vendor text")
			}
		})
	}
}

func TestOutputBearingStreamsSucceed(t *testing.T) {
	index := 0
	textURL := "https://example.com/x.png"
	cases := []struct {
		name   string
		chunks []*schema.Message
		check  func(t *testing.T, res model.Result)
	}{
		{
			name:   "reasoning only",
			chunks: []*schema.Message{{Role: schema.Assistant, ReasoningContent: "think"}},
			check: func(t *testing.T, res model.Result) {
				var msg schema.Message
				_ = json.Unmarshal(res.Message, &msg)
				if msg.ReasoningContent != "think" {
					t.Fatalf("reasoning = %q", msg.ReasoningContent)
				}
			},
		},
		{
			name: "tool calls only",
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					Index: &index, ID: "c1", Type: "function",
					Function: schema.FunctionCall{Name: "fn", Arguments: `{}`},
				}},
			}},
		},
		{
			name: "multi content only",
			chunks: []*schema.Message{{Role: schema.Assistant, AssistantGenMultiContent: []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "pic"},
				{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageOutputImage{
					MessagePartCommon: schema.MessagePartCommon{URL: &textURL},
				}},
			}}},
		},
		{
			name:   "plain text",
			chunks: []*schema.Message{{Role: schema.Assistant, Content: "hello"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdapter(t, &fakeChat{chunks: tc.chunks})
			res, err := model.Invoke(testCtx(t), a, model.Request{
				Model: "sim-chat", Op: model.CapChat, ClientRef: "ok-" + tc.name,
				Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
			})
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if res.State != model.StateSucceeded {
				t.Fatalf("state = %q, want succeeded", res.State)
			}
			if tc.check != nil {
				tc.check(t, res)
			}
		})
	}
}

func TestToolCallExtraMarshalKeepsIdentityOnEvent(t *testing.T) {
	bad := map[string]any{"fn": func() {}}
	index := 0
	f := &fakeChat{chunks: []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index: &index, ID: "call-x", Type: "function", Extra: bad,
			Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`},
		}},
	}},
	}
	a := newAdapter(t, f)
	h, err := a.Start(testCtx(t), model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-tool-extra",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	events := drainEvents(t, a, h)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	var call schema.ToolCall
	if err := json.Unmarshal(events[0].Data, &call); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if call.ID != "call-x" || call.Function.Name != "lookup" {
		t.Fatalf("call = %+v", call)
	}
}

func drainEvents(t *testing.T, a *Adapter, h model.Handle) []model.Event {
	t.Helper()
	ch, err := a.Events(testCtx(t), h, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []model.Event
	for e := range ch {
		out = append(out, e)
	}
	if _, err := a.Result(testCtx(t), h); err != nil {
		t.Fatalf("result: %v", err)
	}
	return out
}
