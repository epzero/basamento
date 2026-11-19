// Package model is the capability-typed model layer. Every model call, chat
// or video, is an invocation with one contract (Invoker): start, events,
// result, cancel, lookup. Vendor protocols (one streamed reply, submit and
// poll, webhook, WebSocket, command line) are adapter details. Callers either
// block (Invoke) or follow events. See ADR-0006 and docs/architecture/model-layer.md.
//
// Chat messages and tool definitions travel as JSON of github.com/cloudwego/eino/schema
// values, so the contract types do not depend on eino itself. The chat adapter
// in model/chat maps them to eino's types; the embedding adapter here maps
// Request.Texts to an eino Embedder. Registry holds the part every adapter owes
// regardless of protocol: idempotent starts, the ordered event log, the
// result, cancellation and lookup.
//
// Provider construction (vendor SDKs, eino-ext, OpenAI-compatible endpoints for
// MiniMax and GLM) and routing are WP-L3-04. Structured-output shaping is
// WP-L3-02.
package model

import (
	"context"
	"encoding/json"
)

// Capability is what a model can do.
type Capability string

const (
	CapChat           Capability = "chat"
	CapEmbed          Capability = "embed"
	CapImageGenerate  Capability = "image.generate"
	CapImageEdit      Capability = "image.edit"
	CapImageUpscale   Capability = "image.upscale"
	CapVideoGenerate  Capability = "video.generate" // text→video and image→video via input slots
	CapVideoLipsync   Capability = "video.lipsync"
	CapAudioTTS       Capability = "audio.tts"
	CapAudioASR       Capability = "audio.asr"
	CapAudioMusic     Capability = "audio.music"
	CapWorkflowGraph  Capability = "workflow.graph"  // graph-based generation servers
	CapBrowserSession Capability = "browser.session" // a browser runner session (ADR-0009)
)

// Protocol is how a vendor delivers results. It only matters inside adapters.
type Protocol string

const (
	ProtocolStream  Protocol = "stream"  // one request, streamed reply (chat, streaming TTS)
	ProtocolSingle  Protocol = "single"  // one request, one reply (embeddings)
	ProtocolPoll    Protocol = "poll"    // submit, then poll
	ProtocolWebhook Protocol = "webhook" // submit, vendor calls back
	ProtocolSocket  Protocol = "socket"  // submit, progress over WebSocket
	ProtocolCommand Protocol = "command" // local command line
)

// Locality says where a model can run.
type Locality string

const (
	LocalityCloud        Locality = "cloud"
	LocalityMachineLocal Locality = "machine_local" // local apps and servers: never routed in the cloud profile
	LocalityWorker       Locality = "worker"        // served by a connected worker that declared it (ADR-0012)
)

// Descriptor is catalog data for one model.
type Descriptor struct {
	ID       string          `json:"id"`
	Provider string          `json:"provider"`
	Caps     []Capability    `json:"caps"`
	Params   json.RawMessage `json:"params_schema"` // JSON Schema; validated before start
	Inputs   []InputSlot     `json:"inputs"`
	Price    string          `json:"price"` // CEL over `params`, `usage` and `rates` returning milli-points
	Protocol Protocol        `json:"protocol"`
	Locality Locality        `json:"locality"`
}

// InputSlot names an input, e.g. prompt, messages, first_frame, reference, audio, mask.
type InputSlot struct {
	Name     string `json:"name"`
	Media    string `json:"media"` // text|messages|image|video|audio
	Required bool   `json:"required"`
	Max      int    `json:"max,omitempty"`
}

// AssetRef points to content-addressed blob storage.
type AssetRef struct {
	BlobKey string `json:"blob_key"`
	SHA256  string `json:"sha256"`
	Media   string `json:"media"`
	Mime    string `json:"mime"`
}

// Request starts an invocation. ClientRef is the idempotency key and is
// identical across retries of the same logical invocation.
type Request struct {
	Model        string                `json:"model"`
	Op           Capability            `json:"op"`
	Messages     json.RawMessage       `json:"messages,omitempty"` // chat transcript (eino schema messages)
	Tools        json.RawMessage       `json:"tools,omitempty"`
	OutputSchema json.RawMessage       `json:"output_schema,omitempty"` // structured output
	Text         map[string]string     `json:"text,omitempty"`
	Texts        []string              `json:"texts,omitempty"` // embedding inputs, in order
	Assets       map[string][]AssetRef `json:"assets,omitempty"`
	Params       json.RawMessage       `json:"params,omitempty"`
	Seed         *int64                `json:"seed,omitempty"`
	ClientRef    string                `json:"client_ref"`
}

// Handle identifies a started invocation.
type Handle struct {
	Provider  string `json:"provider"`
	ID        string `json:"id"` // vendor id, or an adapter-local id for streamed calls
	ClientRef string `json:"client_ref"`
}

// EventKind is the kind of partial output.
type EventKind string

const (
	EventText      EventKind = "text"
	EventReasoning EventKind = "reasoning"
	EventToolCall  EventKind = "tool_call"
	EventAudio     EventKind = "audio"
	EventProgress  EventKind = "progress"
	EventReset     EventKind = "reset" // discard partial output; a new attempt follows
)

// Event is one ordered piece of partial output.
type Event struct {
	Seq      int64           `json:"seq"`
	Kind     EventKind       `json:"kind"`
	Text     string          `json:"text,omitempty"`
	Audio    []byte          `json:"audio,omitempty"`
	Progress float64         `json:"progress,omitempty"`
	Attempt  int             `json:"attempt,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// State is the terminal or current state of an invocation.
type State string

const (
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Usage is token accounting for language models.
type Usage struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CachedTokens    int64 `json:"cached_tokens,omitempty"`
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
}

// Output is a produced file not yet ingested into blob storage.
type Output struct {
	URL   string `json:"url,omitempty"`
	Path  string `json:"path,omitempty"` // command-line providers
	Media string `json:"media"`
	Mime  string `json:"mime"`
}

// Result is how an invocation ended.
type Result struct {
	State    State           `json:"state"`
	Message  json.RawMessage `json:"message,omitempty"` // chat reply or structured output
	Vectors  [][]float32     `json:"vectors,omitempty"` // embeddings
	Outputs  []Output        `json:"outputs,omitempty"` // files
	Usage    *Usage          `json:"usage,omitempty"`
	CostMpts int64           `json:"cost_mpts"`
	Error    json.RawMessage `json:"error,omitempty"`
}

// Invoker is implemented by every provider adapter.
type Invoker interface {
	Describe(ctx context.Context) ([]Descriptor, error)
	// Start begins an invocation and returns at once. It is idempotent per ClientRef.
	Start(ctx context.Context, r Request) (Handle, error)
	// Events delivers partial output with Seq > after; the channel closes when the invocation ends.
	Events(ctx context.Context, h Handle, after int64) (<-chan Event, error)
	// Result waits for the end of the invocation.
	Result(ctx context.Context, h Handle) (Result, error)
	Cancel(ctx context.Context, h Handle) error
	// Lookup resolves an ambiguous start. It returns (nil, nil) when the
	// provider has nothing for clientRef, and an error with core.CodeUnsupported
	// when it cannot answer.
	Lookup(ctx context.Context, clientRef string) (*Handle, error)
}

// Invoke starts an invocation and waits for its result: the blocking form of
// the contract, for callers that do not follow events. The adapter keeps
// publishing into its event buffer while Invoke waits, so a blocking caller
// cannot starve the invocation.
func Invoke(ctx context.Context, p Invoker, r Request) (Result, error) {
	h, err := p.Start(ctx, r)
	if err != nil {
		return Result{}, err
	}
	return p.Result(ctx, h)
}
