package chat

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

type fakeChat struct {
	mu        sync.Mutex
	streams   int
	generates int
	messages  []*schema.Message
	opts      *einomodel.Options
	chunks    []*schema.Message
	streamErr error
	streamFn  func(context.Context) (*schema.StreamReader[*schema.Message], error)
	gotSchema json.RawMessage
	gotSeed   *int64
}

func (f *fakeChat) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	f.mu.Lock()
	f.generates++
	f.mu.Unlock()
	return schema.ConcatMessages(f.chunks)
}

func (f *fakeChat) Stream(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	f.mu.Lock()
	f.streams++
	f.messages = in
	f.opts = einomodel.GetCommonOptions(nil, opts...)
	fn, err, chunks := f.streamFn, f.streamErr, f.chunks
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if fn != nil {
		return fn(ctx)
	}
	cp := make([]*schema.Message, len(chunks))
	copy(cp, chunks)
	return schema.StreamReaderFromArray(cp), nil
}

type schemaChat struct{ *fakeChat }

func (s *schemaChat) WithOutputSchema(raw json.RawMessage) (einomodel.BaseChatModel, error) {
	s.mu.Lock()
	s.gotSchema = append(json.RawMessage(nil), raw...)
	s.mu.Unlock()
	return s, nil
}

type seedChat struct{ *fakeChat }

func (s *seedChat) WithSeed(seed int64) (einomodel.BaseChatModel, error) {
	s.mu.Lock()
	s.gotSeed = &seed
	s.mu.Unlock()
	return s, nil
}

func chatConfig(m einomodel.BaseChatModel) Config {
	return Config{
		Provider: "sim",
		Descriptors: []model.Descriptor{{
			ID:       "sim-chat",
			Provider: "sim",
			Caps:     []model.Capability{model.CapChat},
			Protocol: model.ProtocolStream,
		}},
		Model: m,
	}
}

func messages(t *testing.T, msgs ...*schema.Message) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	return data
}

func newAdapter(t *testing.T, m einomodel.BaseChatModel) *Adapter {
	t.Helper()
	a, err := New(chatConfig(m))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return a
}

func TestStreamNormalizedIntoEvents(t *testing.T) {
	index := 0
	f := &fakeChat{chunks: []*schema.Message{
		{Role: schema.Assistant, Content: "Hel", ReasoningContent: "think"},
		{Role: schema.Assistant, Content: "lo", ToolCalls: []schema.ToolCall{{
			Index: &index, ID: "call-1", Type: "function",
			Function: schema.FunctionCall{Name: "lookup", Arguments: `{"q":"x"}`},
		}}},
		{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 10, CompletionTokens: 4,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 3},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 2},
		}}},
	}}
	a := newAdapter(t, f)
	h, err := a.Start(t.Context(), model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ch, err := a.Events(t.Context(), h, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var got []model.Event
	for e := range ch {
		got = append(got, e)
	}
	res, err := a.Result(t.Context(), h)
	if err != nil {
		t.Fatalf("result: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(got), got)
	}
	wantKinds := []model.EventKind{model.EventText, model.EventReasoning, model.EventText, model.EventToolCall}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("event %d kind = %q, want %q", i, got[i].Kind, want)
		}
		if got[i].Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, got[i].Seq, i+1)
		}
	}
	if got[0].Text != "Hel" || got[1].Text != "think" || got[2].Text != "lo" {
		t.Fatalf("event text = %+v", got)
	}
	var call schema.ToolCall
	if err := json.Unmarshal(got[3].Data, &call); err != nil {
		t.Fatalf("decode tool call: %v", err)
	}
	if call.Function.Name != "lookup" || call.Function.Arguments != `{"q":"x"}` {
		t.Fatalf("tool call = %+v", call)
	}

	if res.State != model.StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
	var msg schema.Message
	if err := json.Unmarshal(res.Message, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.Content != "Hello" {
		t.Fatalf("message = %q, want Hello", msg.Content)
	}
	if res.Usage == nil || *res.Usage != (model.Usage{InputTokens: 10, OutputTokens: 4, CachedTokens: 3, ReasoningTokens: 2}) {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestInvokeReturnsTheFinalMessage(t *testing.T) {
	f := &fakeChat{chunks: []*schema.Message{
		{Role: schema.Assistant, Content: "Hello"},
		{Role: schema.Assistant, Content: " there"},
	}}
	a := newAdapter(t, f)
	res, err := model.Invoke(t.Context(), a, model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	var msg schema.Message
	if err := json.Unmarshal(res.Message, &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Content != "Hello there" {
		t.Fatalf("message = %q, want %q", msg.Content, "Hello there")
	}
}

func TestRequestMapping(t *testing.T) {
	f := &fakeChat{chunks: []*schema.Message{{Role: schema.Assistant, Content: "ok"}}}
	a := newAdapter(t, f)
	tools, err := json.Marshal([]*schema.ToolInfo{{Name: "lookup", Desc: "find"}})
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	params, _ := json.Marshal(map[string]any{
		"temperature": 0.3, "top_p": 0.9, "max_tokens": 100, "stop": []string{"x"},
	})
	h, err := a.Start(t.Context(), model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
		Tools:    tools,
		Params:   params,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := a.Result(t.Context(), h); err != nil {
		t.Fatalf("result: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) != 1 || f.messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", f.messages)
	}
	opts := f.opts
	if opts == nil || opts.Temperature == nil || *opts.Temperature != 0.3 {
		t.Fatalf("temperature = %+v", opts)
	}
	if opts.TopP == nil || *opts.TopP != 0.9 || opts.MaxTokens == nil || *opts.MaxTokens != 100 {
		t.Fatalf("options = %+v", opts)
	}
	if len(opts.Tools) != 1 || opts.Tools[0].Name != "lookup" {
		t.Fatalf("tools = %+v", opts.Tools)
	}
	if len(opts.Stop) != 1 || opts.Stop[0] != "x" {
		t.Fatalf("stop = %+v", opts.Stop)
	}
}

func TestStartRejectsRequestsItCannotServe(t *testing.T) {
	a := newAdapter(t, &fakeChat{})
	valid := model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	}
	cases := []struct {
		name   string
		mutate func(*model.Request)
		code   core.Code
	}{
		{name: "unknown model", mutate: func(r *model.Request) { r.Model = "nope" }, code: core.CodeNotFound},
		{name: "wrong capability", mutate: func(r *model.Request) { r.Op = model.CapEmbed }, code: core.CodeUnsupported},
		{name: "no client ref", mutate: func(r *model.Request) { r.ClientRef = "" }, code: core.CodeInvalidArgument},
		{name: "no messages", mutate: func(r *model.Request) { r.Messages = nil }, code: core.CodeInvalidArgument},
		{name: "bad messages", mutate: func(r *model.Request) { r.Messages = json.RawMessage(`"nope"`) }, code: core.CodeInvalidArgument},
		{name: "bad tools", mutate: func(r *model.Request) { r.Tools = json.RawMessage(`"nope"`) }, code: core.CodeInvalidArgument},
		{name: "bad params", mutate: func(r *model.Request) { r.Params = json.RawMessage(`"nope"`) }, code: core.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)
			if _, err := a.Start(t.Context(), req); core.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q: %v", core.CodeOf(err), tc.code, err)
			}
		})
	}
}

func TestOutputSchemaNeedsAModelThatCarriesIt(t *testing.T) {
	req := model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages:     messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}

	plain := newAdapter(t, &fakeChat{chunks: []*schema.Message{{Content: "x"}}})
	if _, err := plain.Start(t.Context(), req); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("plain model code = %q, want unsupported", core.CodeOf(err))
	}

	f := &fakeChat{chunks: []*schema.Message{{Content: "x"}}}
	withSchema := newAdapter(t, &schemaChat{fakeChat: f})
	h, err := withSchema.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("start with schema: %v", err)
	}
	if _, err := withSchema.Result(t.Context(), h); err != nil {
		t.Fatalf("result: %v", err)
	}
	if string(f.gotSchema) != `{"type":"object"}` {
		t.Fatalf("schema = %s", f.gotSchema)
	}
}

func TestSeedNeedsAModelThatCarriesIt(t *testing.T) {
	seed := int64(7)
	req := model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
		Seed:     &seed,
	}
	plain := newAdapter(t, &fakeChat{chunks: []*schema.Message{{Content: "x"}}})
	if _, err := plain.Start(t.Context(), req); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("plain model code = %q, want unsupported", core.CodeOf(err))
	}

	f := &fakeChat{chunks: []*schema.Message{{Content: "x"}}}
	withSeed := newAdapter(t, &seedChat{fakeChat: f})
	h, err := withSeed.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("start with seed: %v", err)
	}
	if _, err := withSeed.Result(t.Context(), h); err != nil {
		t.Fatalf("result: %v", err)
	}
	if f.gotSeed == nil || *f.gotSeed != seed {
		t.Fatalf("seed = %v", f.gotSeed)
	}
}

func TestStreamFailureEndsTheInvocation(t *testing.T) {
	a := newAdapter(t, &fakeChat{streamErr: errors.New("boom")})
	req := model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	}
	res, err := model.Invoke(t.Context(), a, req)
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
}

func TestErrorMidStreamEndsTheInvocation(t *testing.T) {
	src := schema.StreamReaderFromArray([]*schema.Message{{Content: "a"}})
	failing := schema.StreamReaderWithConvert(src, func(*schema.Message) (*schema.Message, error) {
		return nil, errors.New("reset by peer")
	})
	f := &fakeChat{streamFn: func(context.Context) (*schema.StreamReader[*schema.Message], error) {
		return failing, nil
	}}
	a := newAdapter(t, f)
	req := model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	}
	res, err := model.Invoke(t.Context(), a, req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.State != model.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
}

func TestCancelEndsTheInvocation(t *testing.T) {
	f := &fakeChat{streamFn: func(ctx context.Context) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			<-ctx.Done()
			writer.Close()
		}()
		return reader, nil
	}}
	a := newAdapter(t, f)
	h, err := a.Start(t.Context(), model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := a.Cancel(t.Context(), h); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	res, err := a.Result(t.Context(), h)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.State != model.StateCancelled {
		t.Fatalf("state = %q, want cancelled", res.State)
	}
}

func TestStartIsIdempotent(t *testing.T) {
	f := &fakeChat{chunks: []*schema.Message{{Content: "x"}}}
	a := newAdapter(t, f)
	req := model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "ref-1",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	}
	first, err := a.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	second, err := a.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if first != second {
		t.Fatalf("handles differ: %+v and %+v", first, second)
	}
	if _, err := a.Result(t.Context(), first); err != nil {
		t.Fatalf("result: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.streams != 1 {
		t.Fatalf("stream calls = %d, want 1", f.streams)
	}
}

func TestNewValidatesItsConfig(t *testing.T) {
	if _, err := New(Config{Model: &fakeChat{}}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing provider code = %q", core.CodeOf(err))
	}
	if _, err := New(Config{Provider: "sim"}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing model code = %q", core.CodeOf(err))
	}
	cfg := chatConfig(&fakeChat{})
	cfg.Descriptors = []model.Descriptor{{ID: "x", Caps: []model.Capability{model.CapEmbed}}}
	if _, err := New(cfg); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("missing chat descriptor code = %q", core.CodeOf(err))
	}
}

func TestDescribe(t *testing.T) {
	a := newAdapter(t, &fakeChat{})
	ds, err := a.Describe(t.Context())
	if err != nil || len(ds) != 1 || ds[0].ID != "sim-chat" {
		t.Fatalf("describe = (%+v, %v)", ds, err)
	}
}
