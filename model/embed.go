package model

import (
	"context"
	"fmt"

	einoEmbedding "github.com/cloudwego/eino/components/embedding"

	"github.com/epzero/basamento/core"
)

// EmbeddingConfig describes one embedding adapter.
type EmbeddingConfig struct {
	// Provider names the vendor on every handle and descriptor.
	Provider string
	// Descriptors are the models this adapter serves. Each must declare the
	// embed capability; Select routes a request to one by id and capability.
	Descriptors []Descriptor
	// Embedder is the eino embedding model the adapter drives. Every eino-ext
	// embedding provider satisfies it.
	Embedder einoEmbedding.Embedder
	// Clock is the adapter registry's clock, read for the end times and the
	// retention window of ended invocations. It is required.
	Clock core.Clock
}

// Embedding adapts an eino embedding model to the Invoker contract. It answers
// a request with a single reply: start sends the batch, and the result carries
// the vectors, one per input text in order (the "single" protocol of
// docs/architecture/model-layer.md §1). It publishes no events.
type Embedding struct {
	descriptors []Descriptor
	embedder    einoEmbedding.Embedder
	runs        *Registry
}

var _ Invoker = (*Embedding)(nil)

// NewEmbedding returns an embedding adapter. It fails with "invalid_argument"
// when a dependency is missing and "unsupported" when no descriptor declares
// the embed capability.
func NewEmbedding(cfg EmbeddingConfig) (*Embedding, error) {
	if cfg.Provider == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "embedding adapter has no provider", nil)
	}
	if cfg.Embedder == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "embedding adapter has no embedder", nil)
	}
	if cfg.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "embedding adapter has no clock", nil)
	}
	if !anySupports(cfg.Descriptors, CapEmbed) {
		return nil, core.NewError(core.CodeUnsupported, "no descriptor declares the embed capability", nil)
	}
	runs, err := NewRegistry(cfg.Provider, WithClock(cfg.Clock))
	if err != nil {
		return nil, err
	}
	return &Embedding{
		descriptors: cfg.Descriptors,
		embedder:    cfg.Embedder,
		runs:        runs,
	}, nil
}

// Describe lists the models this adapter serves.
func (e *Embedding) Describe(context.Context) ([]Descriptor, error) {
	return append([]Descriptor(nil), e.descriptors...), nil
}

// Start sends the embedding batch and returns at once. It is idempotent per
// ClientRef while the adapter's registry retains the invocation: a repeated
// start returns the original handle. Once the client ref is evicted, a start
// opens a new invocation (REQ-MODEL-088).
func (e *Embedding) Start(ctx context.Context, r Request) (Handle, error) {
	if _, ok := core.TenantFrom(ctx); !ok {
		return Handle{}, core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	if _, err := Select(e.descriptors, r.Model, r.Op); err != nil {
		return Handle{}, err
	}
	if r.Op != CapEmbed {
		return Handle{}, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("embedding adapter cannot run %s", r.Op), map[string]any{"op": string(r.Op)})
	}
	if r.ClientRef == "" {
		return Handle{}, core.NewError(core.CodeInvalidArgument, "request has no client ref", nil)
	}
	if len(r.Texts) == 0 {
		return Handle{}, core.NewError(core.CodeInvalidArgument, "request has no input texts", nil)
	}
	run, existing, err := e.runs.Begin(ctx, r.ClientRef)
	if err != nil {
		return Handle{}, err
	}
	if existing {
		return run.Handle(), nil
	}
	go e.execute(run, append([]string(nil), r.Texts...))
	return run.Handle(), nil
}

// Events follows any progress the adapter publishes. Embeddings publish none;
// the channel closes when the invocation ends.
func (e *Embedding) Events(ctx context.Context, h Handle, after int64) (<-chan Event, error) {
	return e.runs.Events(ctx, h, after)
}

// Result waits for the vectors.
func (e *Embedding) Result(ctx context.Context, h Handle) (Result, error) {
	return e.runs.Result(ctx, h)
}

// Cancel ends the invocation as cancelled.
func (e *Embedding) Cancel(ctx context.Context, h Handle) error {
	return e.runs.Cancel(ctx, h)
}

// Lookup resolves a client ref to its invocation.
func (e *Embedding) Lookup(ctx context.Context, clientRef string) (*Handle, error) {
	return e.runs.Lookup(ctx, clientRef)
}

func (e *Embedding) execute(run *Run, texts []string) {
	defer func() {
		if p := recover(); p != nil {
			// Deliberately converts a panic at the invocation boundary;
			// run.Fail is a no-op after the run ended.
			var cause error
			if perr, ok := p.(error); ok {
				cause = perr
			}
			run.Fail(&core.Error{Code: core.CodeInternal, Message: "vendor call panicked", Cause: cause})
		}
	}()

	vectors, err := e.embedder.EmbedStrings(run.Context(), texts)
	if err != nil {
		run.Fail(MapError(err))
		return
	}
	if len(vectors) != len(texts) {
		run.Fail(core.NewError(core.CodeVendor,
			fmt.Sprintf("embedding count mismatch: got %d vectors for %d texts", len(vectors), len(texts)), nil))
		return
	}
	out := make([][]float32, len(vectors))
	for i, v := range vectors {
		if len(v) == 0 {
			run.Fail(core.NewError(core.CodeVendor,
				fmt.Sprintf("embedding vector %d is empty", i), nil))
			return
		}
		out[i] = vector32(v)
	}
	run.Succeed(Result{Vectors: out})
}

// anySupports reports whether ds declares op on any descriptor.
func anySupports(ds []Descriptor, op Capability) bool {
	for _, d := range ds {
		for _, c := range d.Caps {
			if c == op {
				return true
			}
		}
	}
	return false
}

// vector32 narrows an eino embedding, whose values are float64, to the
// contract's float32.
func vector32(v []float64) []float32 {
	if v == nil {
		return nil
	}
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(f)
	}
	return out
}
