package model

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestEmbeddingWrongProviderHandleNotFound(t *testing.T) {
	embedder := &fakeEmbedder{vecs: [][]float64{{1}}}
	a, err := NewEmbedding(embedConfig(embedder))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	h, err := a.Start(testCtx(t), Request{Model: "sim-embed", Op: CapEmbed, Texts: []string{"x"}, ClientRef: "r"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	h.Provider = "other"
	if _, err := a.Result(testCtx(t), h); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("result code = %q, want not_found", core.CodeOf(err))
	}
}
