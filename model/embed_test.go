package model

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	einoEmbedding "github.com/cloudwego/eino/components/embedding"

	"github.com/epzero/basamento/core"
)

type fakeEmbedder struct {
	mu    sync.Mutex
	calls int
	texts []string
	vecs  [][]float64
	err   error
}

func (f *fakeEmbedder) EmbedStrings(ctx context.Context, texts []string, _ ...einoEmbedding.Option) ([][]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.texts = append([]string(nil), texts...)
	if f.err != nil {
		return nil, f.err
	}
	return f.vecs, nil
}

func embedConfig(f *fakeEmbedder) EmbeddingConfig {
	return EmbeddingConfig{
		Provider: "sim",
		Descriptors: []Descriptor{{
			ID:       "sim-embed",
			Provider: "sim",
			Caps:     []Capability{CapEmbed},
			Protocol: ProtocolSingle,
		}},
		Embedder: f,
	}
}

func TestEmbeddingReturnsVectorsInOrder(t *testing.T) {
	f := &fakeEmbedder{vecs: [][]float64{{0.5, 1.5}, {2.5}}}
	adapter, err := NewEmbedding(embedConfig(f))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	res, err := Invoke(t.Context(), adapter, Request{
		Model:     "sim-embed",
		Op:        CapEmbed,
		Texts:     []string{"one", "two"},
		ClientRef: "ref-1",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
	want := [][]float32{{0.5, 1.5}, {2.5}}
	if !reflect.DeepEqual(res.Vectors, want) {
		t.Fatalf("vectors = %v, want %v", res.Vectors, want)
	}
	if !reflect.DeepEqual(f.texts, []string{"one", "two"}) {
		t.Fatalf("embedder texts = %v", f.texts)
	}
}

func TestEmbeddingPublishesNoEvents(t *testing.T) {
	f := &fakeEmbedder{vecs: [][]float64{{1}}}
	adapter, _ := NewEmbedding(embedConfig(f))
	h, err := adapter.Start(t.Context(), Request{Model: "sim-embed", Op: CapEmbed, Texts: []string{"x"}, ClientRef: "ref-1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := adapter.Result(t.Context(), h); err != nil {
		t.Fatalf("result: %v", err)
	}
	ch, err := adapter.Events(t.Context(), h, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var events []Event
	for e := range ch {
		events = append(events, e)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}

func TestEmbeddingStartIsIdempotent(t *testing.T) {
	f := &fakeEmbedder{vecs: [][]float64{{1}}}
	adapter, _ := NewEmbedding(embedConfig(f))
	req := Request{Model: "sim-embed", Op: CapEmbed, Texts: []string{"x"}, ClientRef: "ref-1"}
	first, err := adapter.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	second, err := adapter.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if first != second {
		t.Fatalf("handles differ: %+v and %+v", first, second)
	}
	if _, err := adapter.Result(t.Context(), first); err != nil {
		t.Fatalf("result: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("embedder called %d times, want 1", f.calls)
	}
}

func TestEmbeddingRejectsRequestsItCannotServe(t *testing.T) {
	adapter, _ := NewEmbedding(embedConfig(&fakeEmbedder{}))
	cases := []struct {
		name string
		req  Request
		code core.Code
	}{
		{name: "unknown model", req: Request{Model: "nope", Op: CapEmbed, Texts: []string{"x"}, ClientRef: "r"}, code: core.CodeNotFound},
		{name: "wrong capability", req: Request{Model: "sim-embed", Op: CapChat, Texts: []string{"x"}, ClientRef: "r"}, code: core.CodeUnsupported},
		{name: "no client ref", req: Request{Model: "sim-embed", Op: CapEmbed, Texts: []string{"x"}}, code: core.CodeInvalidArgument},
		{name: "no texts", req: Request{Model: "sim-embed", Op: CapEmbed, ClientRef: "r"}, code: core.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adapter.Start(t.Context(), tc.req); core.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q: %v", core.CodeOf(err), tc.code, err)
			}
		})
	}
}

func TestEmbeddingVendorFailure(t *testing.T) {
	adapter, _ := NewEmbedding(embedConfig(&fakeEmbedder{err: errors.New("boom")}))
	res, err := Invoke(t.Context(), adapter, Request{Model: "sim-embed", Op: CapEmbed, Texts: []string{"x"}, ClientRef: "r"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	var failure Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failure.Code != core.CodeVendor {
		t.Fatalf("code = %q, want vendor_error", failure.Code)
	}
}

func TestNewEmbeddingValidatesItsConfig(t *testing.T) {
	if _, err := NewEmbedding(EmbeddingConfig{Embedder: &fakeEmbedder{}}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing provider code = %q", core.CodeOf(err))
	}
	if _, err := NewEmbedding(EmbeddingConfig{Provider: "sim"}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing embedder code = %q", core.CodeOf(err))
	}
	if _, err := NewEmbedding(EmbeddingConfig{Provider: "sim", Embedder: &fakeEmbedder{}}); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("missing descriptor code = %q", core.CodeOf(err))
	}
}

func TestEmbeddingDescribe(t *testing.T) {
	adapter, _ := NewEmbedding(embedConfig(&fakeEmbedder{}))
	ds, err := adapter.Describe(t.Context())
	if err != nil || len(ds) != 1 || ds[0].ID != "sim-embed" {
		t.Fatalf("describe = (%+v, %v)", ds, err)
	}
}
