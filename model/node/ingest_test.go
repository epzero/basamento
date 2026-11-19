package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/blob"
)

// memBlob is a content store held in memory.
type memBlob struct {
	data map[string][]byte
	typ  map[string]string
	puts int
}

func newMemBlob() *memBlob {
	return &memBlob{data: map[string][]byte{}, typ: map[string]string{}}
}

func (m *memBlob) Put(_ context.Context, key string, r io.Reader, contentType string) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.data[key] = body
	m.typ[key] = contentType
	m.puts++
	return nil
}

func (m *memBlob) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, core.NewError(core.CodeNotFound, "not found", nil)
}

func (m *memBlob) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

func (m *memBlob) Delete(context.Context, string) error { return nil }

func (m *memBlob) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (m *memBlob) StartUpload(context.Context, platform.UploadOptions) (platform.Upload, error) {
	return platform.Upload{}, core.NewError(core.CodeUnsupported, "uploads are not supported by the test store", nil)
}

func (m *memBlob) Commit(context.Context, string) (string, error) {
	return "", core.NewError(core.CodeUnsupported, "uploads are not supported by the test store", nil)
}

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.New(core.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testHandler(t *testing.T, d Deps) *Handler {
	t.Helper()
	if d.Invoker == nil {
		d.Invoker = newScriptInvoker()
	}
	if d.Catalog == nil {
		d.Catalog = testCatalog(t)
	}
	h, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestIngestStoresOutputsByContentHashWithProvenance(t *testing.T) {
	t.Parallel()
	blobs := newMemBlob()
	files := map[string][]byte{
		"https://vendor.test/a.png": []byte("first image"),
		"https://vendor.test/b.png": []byte("second image"),
	}
	h := testHandler(t, Deps{
		Blobs: blobs,
		Fetch: func(_ context.Context, out model.Output) ([]byte, string, error) {
			return files[out.URL], "image/png", nil
		},
	})
	seed := int64(42)
	in := ingestIn{
		Model:  "sim-image",
		Op:     "image.generate",
		Params: json.RawMessage(`{"size":"1024"}`),
		Seed:   &seed,
		Cost:   480,
		Output: []model.Output{
			{URL: "https://vendor.test/a.png", Media: "image", Mime: "image/png"},
			{URL: "https://vendor.test/b.png", Media: "image", Mime: "image/png"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.ingestStep(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Assets []Asset `json:"assets"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(got.Assets))
	}
	for i, asset := range got.Assets {
		data := files[in.Output[i].URL]
		if asset.Ref.BlobKey != blob.ContentKeyOf(data) {
			t.Fatalf("asset %d key = %q, want its content hash", i, asset.Ref.BlobKey)
		}
		if !bytes.Equal(blobs.data[asset.Ref.BlobKey], data) {
			t.Fatalf("asset %d bytes were not stored", i)
		}
		if asset.Provenance.Model != "sim-image" || asset.Provenance.CostMpts != 480 {
			t.Fatalf("asset %d provenance = %+v", i, asset.Provenance)
		}
		if asset.Provenance.Seed == nil || *asset.Provenance.Seed != 42 {
			t.Fatalf("asset %d seed = %v, want 42", i, asset.Provenance.Seed)
		}
		if asset.Provenance.ParamsHash != ParamHash(in.Params) {
			t.Fatalf("asset %d params hash = %q", i, asset.Provenance.ParamsHash)
		}
		var stored Provenance
		if err := json.Unmarshal(blobs.data[asset.ProvenanceBlobKey], &stored); err != nil {
			t.Fatalf("asset %d provenance was not stored: %v", i, err)
		}
		if stored.Model != asset.Provenance.Model || stored.Op != asset.Provenance.Op ||
			stored.ParamsHash != asset.Provenance.ParamsHash || stored.CostMpts != asset.Provenance.CostMpts ||
			stored.Media != asset.Provenance.Media || stored.Mime != asset.Provenance.Mime ||
			stored.Seed == nil || *stored.Seed != *asset.Provenance.Seed {
			t.Fatalf("asset %d stored provenance = %+v, want %+v", i, stored, asset.Provenance)
		}
	}
}

func TestIngestReadsACommandOutputFromItsPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/frame.png"
	if err := os.WriteFile(path, []byte("frame"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobs := newMemBlob()
	h := testHandler(t, Deps{Blobs: blobs})
	raw, err := json.Marshal(ingestIn{
		Model:  "local-video",
		Op:     "video.generate",
		Output: []model.Output{{Path: path, Media: "video", Mime: "image/png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.ingestStep(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Assets []Asset `json:"assets"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Assets) != 1 || got.Assets[0].Ref.BlobKey != blob.ContentKeyOf([]byte("frame")) {
		t.Fatalf("assets = %+v", got.Assets)
	}
}

func TestParamHashIsStableAndCanonical(t *testing.T) {
	t.Parallel()
	a := ParamHash(json.RawMessage(`{"b":2,"a":1}`))
	b := ParamHash(json.RawMessage(`{ "a": 1, "b": 2 }`))
	if a != b {
		t.Fatalf("canonical params hash differently: %q != %q", a, b)
	}
	if a == ParamHash(json.RawMessage(`{"a":1,"b":3}`)) {
		t.Fatal("different params hash alike")
	}
	if ParamHash(nil) != ParamHash(json.RawMessage(`{}`)) {
		t.Fatal("absent params should hash as an empty object")
	}
}
