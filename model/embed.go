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
}

// Embedding adapts an eino embedding model to the Invoker contract. It answers
// a request with a single reply: start sends the batch, and the result carries
// the vectors, one per input text in order (the "single" protocol of
// docs/architecture/model-layer.md §1). It publishes no events.
type Embedding struct {
	provider    string
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
	if !anySupports(cfg.Descriptors, CapEmbed) {
		return nil, core.NewError(core.CodeUnsupported, "no descriptor declares the embed capability", nil)
	}
	return &Embedding{
		provider:    cfg.Provider,
		descriptors: cfg.Descriptors,
		embedder:    cfg.Embedder,
		runs:        NewRegistry(cfg.Provider),
	}, nil
}

// Describe lists the models this adapter serves.
func (e *Embedding) Describe(context.Context) ([]Descriptor, error) {
	return append([]Descriptor(nil), e.descriptors...), nil
}

// Start sends the embedding batch and returns at once. It is idempotent per
// ClientRef: a repeated start returns the original handle.
func (e *Embedding) Start(ctx context.Context, r Request) (Handle, error) {
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
	run, existing := e.runs.Begin(ctx, r.ClientRef)
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
	vectors, err := e.embedder.EmbedStrings(run.Context(), texts)
	if err != nil {
		run.Fail(MapError(err))
		return
	}
	out := make([][]float32, len(vectors))
	for i, v := range vectors {
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
