package steps

import (
	"context"
	"encoding/json"
	"fmt"

	einoEmbedding "github.com/cloudwego/eino/components/embedding"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/chat"
)

type modelWorld struct {
	invoker    model.Invoker
	capability model.Capability
	modelID    string

	streamText   string
	streamChunks int
	hold         bool

	// retention is the registry the REQ-MODEL-088 scenarios bound and drive
	// directly. Its reg is nil until such a scenario builds one.
	retention *retentionWorld

	// accepted counts the starts a simulated adapter passed to its vendor, so
	// "the provider accepted N invocations" answers for them.
	accepted int

	events []model.Event
	result model.Result
}

func newModelWorld() *modelWorld { return &modelWorld{retention: newRetentionWorld()} }

func (w *modelWorld) tenantCtx(ctx context.Context) context.Context {
	if _, ok := core.TenantFrom(ctx); ok {
		return ctx
	}
	return core.WithTenant(ctx, "bdd-models")
}

func registerModel(sc *godog.ScenarioContext) {
	sc.Step(`^a simulated "([^"]*)" provider whose vendor protocol is "([^"]*)"$`, func(ctx context.Context, capability, protocol string) error {
		return world(ctx).Model.provider(ctx, capability, protocol)
	})
	sc.Step(`^a simulated "([^"]*)" provider that streams "([^"]*)" in (\d+) events$`, func(ctx context.Context, capability, text string, chunks int) error {
		w := world(ctx).Model
		w.streamText, w.streamChunks = text, chunks
		return w.provider(ctx, capability, "streamed reply")
	})
	sc.Step(`^a caller starts an invocation and follows its events$`, func(ctx context.Context) error {
		return world(ctx).Model.follow(ctx)
	})
	sc.Step(`^a caller starts an invocation and cancels it after its first event$`, func(ctx context.Context) error {
		w := world(ctx).Model
		w.hold = true
		return w.cancelAfterFirst(ctx)
	})
	sc.Step(`^a caller invokes it and waits without following events$`, func(ctx context.Context) error {
		w := world(ctx).Model
		req, err := w.request()
		if err != nil {
			return err
		}
		w.result = model.Result{}
		res, err := model.Invoke(w.tenantCtx(ctx), w.invoker, req)
		w.result = res
		return err
	})
	sc.Step(`^the caller receives ordered events and then a result with state "([^"]*)"$`, func(ctx context.Context, state string) error {
		w := world(ctx).Model
		if err := orderedEvents(w.events); err != nil {
			return err
		}
		if w.capability != model.CapEmbed && len(w.events) == 0 {
			return fmt.Errorf("received no events before the result")
		}
		if got := string(w.result.State); got != state {
			return fmt.Errorf("result state is %q, want %q", got, state)
		}
		return nil
	})
	sc.Step(`^the invocation ends with state "([^"]*)"$`, func(ctx context.Context, state string) error {
		if got := string(world(ctx).Model.result.State); got != state {
			return fmt.Errorf("invocation state is %q, want %q", got, state)
		}
		return nil
	})
	sc.Step(`^the result message text is "([^"]*)"$`, func(ctx context.Context, text string) error {
		w := world(ctx).Model
		if w.result.State != model.StateSucceeded {
			return fmt.Errorf("result state is %q, want succeeded", w.result.State)
		}
		var msg schema.Message
		if err := json.Unmarshal(w.result.Message, &msg); err != nil {
			return fmt.Errorf("result message is not an eino message: %w", err)
		}
		if msg.Content != text {
			return fmt.Errorf("result message text is %q, want %q", msg.Content, text)
		}
		return nil
	})
	sc.Step(`^the caller receives (\d+) text events in order$`, func(ctx context.Context, want int) error {
		w := world(ctx).Model
		var text string
		count := 0
		for _, e := range w.events {
			if e.Kind != model.EventText {
				continue
			}
			count++
			text += e.Text
		}
		if count != want {
			return fmt.Errorf("received %d text events, want %d", count, want)
		}
		if text != w.streamText {
			return fmt.Errorf("text events add up to %q, want %q", text, w.streamText)
		}
		return nil
	})
}

// provider builds the adapter the scenario's capability needs. Chat and
// embeddings go through the production adapters, over a simulated eino model;
// the other capabilities use a protocol simulator.
func (w *modelWorld) provider(ctx context.Context, capability, protocol string) error {
	proto, err := parseProtocol(protocol)
	if err != nil {
		return err
	}
	w.capability = model.Capability(capability)
	w.modelID = "sim-" + capability
	if w.streamText == "" {
		w.streamText, w.streamChunks = "Hello there", 3
	}
	desc := model.Descriptor{
		ID:       w.modelID,
		Provider: "sim",
		Caps:     []model.Capability{w.capability},
		Protocol: proto,
	}
	switch w.capability {
	case model.CapChat:
		adapter, err := chat.New(chat.Config{
			Provider:    "sim",
			Descriptors: []model.Descriptor{desc},
			Model:       &simChatModel{chunks: chatChunks(w.streamText, w.streamChunks)},
			Clock:       world(ctx).Core.clock,
		})
		if err != nil {
			return err
		}
		w.invoker = adapter
	case model.CapEmbed:
		adapter, err := model.NewEmbedding(model.EmbeddingConfig{
			Provider:    "sim",
			Descriptors: []model.Descriptor{desc},
			Embedder:    &simEmbedder{},
			Clock:       world(ctx).Core.clock,
		})
		if err != nil {
			return err
		}
		w.invoker = adapter
	default:
		runs, err := model.NewRegistry("sim", model.WithClock(world(ctx).Core.clock))
		if err != nil {
			return err
		}
		w.invoker = &simProvider{
			desc:     desc,
			runs:     runs,
			accepted: &w.accepted,
			hold:     &w.hold,
			text:     w.streamText,
			chunks:   w.streamChunks,
			vectors:  [][]float32{{0.25, 0.5}},
		}
	}
	return nil
}

func (w *modelWorld) request() (model.Request, error) {
	r := model.Request{Model: w.modelID, Op: w.capability, ClientRef: "sim-ref"}
	switch w.capability {
	case model.CapChat:
		data, err := json.Marshal([]*schema.Message{{Role: schema.User, Content: "hi"}})
		if err != nil {
			return model.Request{}, err
		}
		r.Messages = data
	case model.CapEmbed:
		r.Texts = []string{"hello"}
	}
	return r, nil
}

func (w *modelWorld) follow(ctx context.Context) error {
	w.events = nil
	req, err := w.request()
	if err != nil {
		return err
	}
	ctx = w.tenantCtx(ctx)
	h, err := w.invoker.Start(ctx, req)
	if err != nil {
		return err
	}
	ch, err := w.invoker.Events(ctx, h, 0)
	if err != nil {
		return err
	}
	for e := range ch {
		w.events = append(w.events, e)
	}
	w.result, err = w.invoker.Result(ctx, h)
	return err
}

func (w *modelWorld) cancelAfterFirst(ctx context.Context) error {
	w.events = nil
	req, err := w.request()
	if err != nil {
		return err
	}
	ctx = w.tenantCtx(ctx)
	h, err := w.invoker.Start(ctx, req)
	if err != nil {
		return err
	}
	ch, err := w.invoker.Events(ctx, h, 0)
	if err != nil {
		return err
	}
	first, ok := <-ch
	if !ok {
		return fmt.Errorf("the invocation ended without an event to cancel after")
	}
	w.events = append(w.events, first)
	if err := w.invoker.Cancel(ctx, h); err != nil {
		return err
	}
	for e := range ch {
		w.events = append(w.events, e)
	}
	w.result, err = w.invoker.Result(ctx, h)
	return err
}

func parseProtocol(s string) (model.Protocol, error) {
	switch s {
	case "streamed reply":
		return model.ProtocolStream, nil
	case "single reply":
		return model.ProtocolSingle, nil
	case "submit and poll":
		return model.ProtocolPoll, nil
	case "webhook":
		return model.ProtocolWebhook, nil
	case "websocket":
		return model.ProtocolSocket, nil
	case "command line":
		return model.ProtocolCommand, nil
	default:
		return "", fmt.Errorf("unknown vendor protocol %q", s)
	}
}

// textChunks splits text into n chunks that concatenate back to text. The
// adapters publish one event per non-empty chunk, so len(text) >= n gives
// exactly n events.
func textChunks(text string, n int) []string {
	if n <= 0 || text == "" {
		return nil
	}
	runes := []rune(text)
	chunks := make([]string, 0, n)
	for i := 0; i < n; i++ {
		start := i * len(runes) / n
		end := (i + 1) * len(runes) / n
		if start == end {
			continue
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func orderedEvents(events []model.Event) error {
	for i, e := range events {
		if e.Seq != int64(i+1) {
			return fmt.Errorf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
	return nil
}

// chatChunks is textChunks as assistant message deltas, so a simulated chat
// stream has one chunk per event.
func chatChunks(text string, n int) []*schema.Message {
	parts := textChunks(text, n)
	chunks := make([]*schema.Message, len(parts))
	for i, part := range parts {
		chunks[i] = &schema.Message{Role: schema.Assistant, Content: part}
	}
	return chunks
}

// simChatModel is an eino chat model that replays a fixed chunk list.
type simChatModel struct {
	chunks []*schema.Message
}

var _ einomodel.BaseChatModel = (*simChatModel)(nil)

func (m *simChatModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return schema.ConcatMessages(m.chunks)
}

func (m *simChatModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	chunks := make([]*schema.Message, len(m.chunks))
	copy(chunks, m.chunks)
	return schema.StreamReaderFromArray(chunks), nil
}

// simEmbedder returns one deterministic vector per input text.
type simEmbedder struct{}

var _ einoEmbedding.Embedder = (*simEmbedder)(nil)

func (*simEmbedder) EmbedStrings(_ context.Context, texts []string, _ ...einoEmbedding.Option) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	for i := range texts {
		vectors[i] = []float64{float64(len(texts[i])), float64(i)}
	}
	return vectors, nil
}

// simProvider is a vendor protocol simulator. It uses the production registry,
// so the contract's start, events, result, cancel and lookup are the ones the
// adapters give.
type simProvider struct {
	desc     model.Descriptor
	runs     *model.Registry
	accepted *int // one per start that opens a new invocation
	hold     *bool
	text     string
	chunks   int
	vectors  [][]float32
}

var _ model.Invoker = (*simProvider)(nil)

func (p *simProvider) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{p.desc}, nil
}

func (p *simProvider) Start(ctx context.Context, r model.Request) (model.Handle, error) {
	if _, ok := core.TenantFrom(ctx); !ok {
		return model.Handle{}, core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	if r.ClientRef == "" {
		return model.Handle{}, core.NewError(core.CodeInvalidArgument, "request has no client ref", nil)
	}
	run, existing, err := p.runs.Begin(ctx, r.ClientRef)
	if err != nil {
		return model.Handle{}, err
	}
	if existing {
		return run.Handle(), nil
	}
	if p.accepted != nil {
		*p.accepted++
	}
	go p.execute(run)
	return run.Handle(), nil
}

func (p *simProvider) execute(run *model.Run) {
	if p.desc.Protocol == model.ProtocolSingle {
		run.Succeed(model.Result{Vectors: p.vectors})
		return
	}
	if p.desc.Protocol == model.ProtocolStream {
		for _, chunk := range textChunks(p.text, p.chunks) {
			run.Publish(model.Event{Kind: model.EventText, Text: chunk})
		}
		run.Succeed(messageResult(p.text))
		return
	}
	run.Publish(model.Event{Kind: model.EventProgress, Progress: 0.1})
	if p.hold != nil && *p.hold {
		<-run.Done()
		return
	}
	run.Publish(model.Event{Kind: model.EventProgress, Progress: 1})
	run.Succeed(messageResult(p.text))
}

func (p *simProvider) Events(ctx context.Context, h model.Handle, after int64) (<-chan model.Event, error) {
	return p.runs.Events(ctx, h, after)
}

func (p *simProvider) Result(ctx context.Context, h model.Handle) (model.Result, error) {
	return p.runs.Result(ctx, h)
}

func (p *simProvider) Cancel(ctx context.Context, h model.Handle) error {
	return p.runs.Cancel(ctx, h)
}

func (p *simProvider) Lookup(ctx context.Context, clientRef string) (*model.Handle, error) {
	return p.runs.Lookup(ctx, clientRef)
}

func messageResult(text string) model.Result {
	data, err := json.Marshal(&schema.Message{Role: schema.Assistant, Content: text})
	if err != nil {
		return model.Result{State: model.StateFailed}
	}
	return model.Result{Message: data}
}
