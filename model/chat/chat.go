package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// Config describes one chat adapter.
type Config struct {
	// Provider names the vendor on every handle.
	Provider string
	// Descriptors are the models this adapter serves. Each must declare the
	// chat capability; model.Select routes a request by id and capability.
	Descriptors []model.Descriptor
	// Model is the eino chat model the adapter drives.
	Model einomodel.BaseChatModel
}

// Adapter implements model.Invoker over an eino chat model.
type Adapter struct {
	descriptors []model.Descriptor
	chat        einomodel.BaseChatModel
	runs        *model.Registry
}

var _ model.Invoker = (*Adapter)(nil)

// New returns a chat adapter. It fails with "invalid_argument" when a
// dependency is missing and "unsupported" when no descriptor declares the chat
// capability.
func New(cfg Config) (*Adapter, error) {
	if cfg.Provider == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "chat adapter has no provider", nil)
	}
	if cfg.Model == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "chat adapter has no model", nil)
	}
	if !anyChat(cfg.Descriptors) {
		return nil, core.NewError(core.CodeUnsupported, "no descriptor declares the chat capability", nil)
	}
	return &Adapter{
		descriptors: cfg.Descriptors,
		chat:        cfg.Model,
		runs:        model.NewRegistry(cfg.Provider),
	}, nil
}

// Describe lists the models this adapter serves.
func (a *Adapter) Describe(context.Context) ([]model.Descriptor, error) {
	return append([]model.Descriptor(nil), a.descriptors...), nil
}

// Start validates the request, opens the invocation and returns at once. It is
// idempotent per ClientRef: a repeated start returns the original handle and
// publishes nothing more.
func (a *Adapter) Start(ctx context.Context, r model.Request) (model.Handle, error) {
	if _, err := model.Select(a.descriptors, r.Model, r.Op); err != nil {
		return model.Handle{}, err
	}
	if r.Op != model.CapChat {
		return model.Handle{}, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("chat adapter cannot run %s", r.Op), map[string]any{"op": string(r.Op)})
	}
	if r.ClientRef == "" {
		return model.Handle{}, core.NewError(core.CodeInvalidArgument, "request has no client ref", nil)
	}

	messages, err := decodeMessages(r.Messages)
	if err != nil {
		return model.Handle{}, err
	}
	opts, err := callOptions(r)
	if err != nil {
		return model.Handle{}, err
	}
	chat, err := a.modelFor(r)
	if err != nil {
		return model.Handle{}, err
	}

	run, existing := a.runs.Begin(ctx, r.ClientRef)
	if existing {
		return run.Handle(), nil
	}
	go a.execute(run, chat, messages, opts)
	return run.Handle(), nil
}

// Events follows the stream's normalized events with Seq > after.
func (a *Adapter) Events(ctx context.Context, h model.Handle, after int64) (<-chan model.Event, error) {
	return a.runs.Events(ctx, h, after)
}

// Result waits for the assembled message.
func (a *Adapter) Result(ctx context.Context, h model.Handle) (model.Result, error) {
	return a.runs.Result(ctx, h)
}

// Cancel declares the invocation cancelled and cancels the vendor stream.
func (a *Adapter) Cancel(ctx context.Context, h model.Handle) error {
	return a.runs.Cancel(ctx, h)
}

// Lookup resolves a client ref to its invocation.
func (a *Adapter) Lookup(ctx context.Context, clientRef string) (*model.Handle, error) {
	return a.runs.Lookup(ctx, clientRef)
}

// modelFor applies the request's structured-output schema and seed when it
// carries them. A model that cannot carry one fails with "unsupported" before
// any invocation starts, rather than silently dropping it (REQ-MODEL-046).
func (a *Adapter) modelFor(r model.Request) (einomodel.BaseChatModel, error) {
	chat := a.chat
	if len(r.OutputSchema) > 0 {
		schemaModel, ok := chat.(SchemaModel)
		if !ok {
			return nil, core.NewError(core.CodeUnsupported,
				"model cannot carry an output schema", map[string]any{"model": r.Model})
		}
		var err error
		chat, err = schemaModel.WithOutputSchema(r.OutputSchema)
		if err != nil {
			return nil, model.MapError(err)
		}
	}
	if r.Seed != nil {
		seedModel, ok := chat.(SeedModel)
		if !ok {
			return nil, core.NewError(core.CodeUnsupported,
				"model cannot carry a seed", map[string]any{"model": r.Model})
		}
		var err error
		chat, err = seedModel.WithSeed(*r.Seed)
		if err != nil {
			return nil, model.MapError(err)
		}
	}
	return chat, nil
}

func (a *Adapter) execute(run *model.Run, chat einomodel.BaseChatModel, messages []*schema.Message, opts []einomodel.Option) {
	stream, err := chat.Stream(run.Context(), messages, opts...)
	if err != nil {
		run.Fail(model.MapError(err))
		return
	}
	if stream == nil {
		run.Fail(core.NewError(core.CodeInternal, "chat model returned no stream", nil))
		return
	}
	defer stream.Close()

	var chunks []*schema.Message
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			run.Fail(model.MapError(err))
			return
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)
		publishChunk(run, chunk)
	}

	final, err := schema.ConcatMessages(chunks)
	if err != nil {
		run.Fail(model.MapError(err))
		return
	}
	res := model.Result{Usage: usageOf(chunks)}
	if final != nil {
		data, err := json.Marshal(final)
		if err != nil {
			run.Fail(model.MapError(err))
			return
		}
		res.Message = data
	}
	run.Succeed(res)
}

// SchemaModel is a chat model that carries a JSON output schema to its vendor.
// Provider adapters that support native structured output implement it; a
// model without it fails a schema request instead of answering free text.
type SchemaModel interface {
	einomodel.BaseChatModel
	WithOutputSchema(raw json.RawMessage) (einomodel.BaseChatModel, error)
}

// SeedModel is a chat model that carries a sampling seed to its vendor.
type SeedModel interface {
	einomodel.BaseChatModel
	WithSeed(seed int64) (einomodel.BaseChatModel, error)
}

// publishChunk turns one streamed message chunk into contract events. Chunk
// content is a delta, which is how eino's providers deliver a stream.
func publishChunk(run *model.Run, chunk *schema.Message) {
	if chunk.Content != "" {
		run.Publish(model.Event{Kind: model.EventText, Text: chunk.Content})
	}
	if chunk.ReasoningContent != "" {
		run.Publish(model.Event{Kind: model.EventReasoning, Text: chunk.ReasoningContent})
	}
	for i := range chunk.ToolCalls {
		data, err := json.Marshal(chunk.ToolCalls[i])
		if err != nil {
			// A tool call's Extra is free-form and may hold a value JSON cannot
			// encode; the assembled result still carries the call.
			continue
		}
		run.Publish(model.Event{Kind: model.EventToolCall, Data: data})
	}
}

// usageOf returns the last usage a chunk reported. A provider that reports
// none leaves the result without usage, which metering prices as zero
// (REQ-MODEL-061).
func usageOf(chunks []*schema.Message) *model.Usage {
	for i := len(chunks) - 1; i >= 0; i-- {
		meta := chunks[i].ResponseMeta
		if meta == nil || meta.Usage == nil {
			continue
		}
		u := meta.Usage
		return &model.Usage{
			InputTokens:     int64(u.PromptTokens),
			OutputTokens:    int64(u.CompletionTokens),
			CachedTokens:    int64(u.PromptTokenDetails.CachedTokens),
			ReasoningTokens: int64(u.CompletionTokensDetails.ReasoningTokens),
		}
	}
	return nil
}

func decodeMessages(raw json.RawMessage) ([]*schema.Message, error) {
	if len(raw) == 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "request has no messages", nil)
	}
	var messages []*schema.Message
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "messages are not eino schema messages", map[string]any{"cause": err.Error()})
	}
	if len(messages) == 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "request has no messages", nil)
	}
	return messages, nil
}

// chatParams is the generic subset of model parameters every eino chat model
// understands. Vendor-specific parameters are the provider adapter's (WP-L3-04).
type chatParams struct {
	Temperature *float32 `json:"temperature"`
	TopP        *float32 `json:"top_p"`
	MaxTokens   *int     `json:"max_tokens"`
	Stop        []string `json:"stop"`
}

func callOptions(r model.Request) ([]einomodel.Option, error) {
	var opts []einomodel.Option
	if len(r.Tools) > 0 {
		var tools []*schema.ToolInfo
		if err := json.Unmarshal(r.Tools, &tools); err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "tools are not eino tool infos", map[string]any{"cause": err.Error()})
		}
		if len(tools) > 0 {
			opts = append(opts, einomodel.WithTools(tools))
		}
	}
	if len(r.Params) == 0 {
		return opts, nil
	}
	var p chatParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "params are not a chat parameter object", map[string]any{"cause": err.Error()})
	}
	if p.Temperature != nil {
		opts = append(opts, einomodel.WithTemperature(*p.Temperature))
	}
	if p.TopP != nil {
		opts = append(opts, einomodel.WithTopP(*p.TopP))
	}
	if p.MaxTokens != nil {
		opts = append(opts, einomodel.WithMaxTokens(*p.MaxTokens))
	}
	if len(p.Stop) > 0 {
		opts = append(opts, einomodel.WithStop(p.Stop))
	}
	return opts, nil
}

func anyChat(ds []model.Descriptor) bool {
	for _, d := range ds {
		for _, c := range d.Caps {
			if c == model.CapChat {
				return true
			}
		}
	}
	return false
}
