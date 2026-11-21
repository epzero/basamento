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

// An adapter records its vendor id on the run, and Lookup then carries it, so
// a provider needs no id table of its own (REQ-MODEL-088).
func TestLookupCarriesTheAdapterVendorID(t *testing.T) {
	reg := newTestRegistry(t)
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.SetID("job-7")
	h, err := reg.Lookup(testCtx(t), "ref-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if h == nil || h.ID != "job-7" || h.ClientRef != "ref-1" {
		t.Fatalf("lookup = %+v, want vendor id job-7", h)
	}
}
