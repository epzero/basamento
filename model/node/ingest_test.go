package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	return platform.Upload{}, core.NewError(core.CodeUnavailable, "staging is not used in this test", nil)
}

func (m *memBlob) Commit(context.Context, string) (string, error) {
	return "", core.NewError(core.CodeUnavailable, "staging is not used in this test", nil)
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
	// A command-line provider's path is only read under the configured root.
	h := testHandler(t, Deps{Blobs: blobs, OutputRoot: dir})
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

func TestDefaultFetcherRequiresHTTPS(t *testing.T) {
	t.Parallel()
	_, _, err := defaultFetcher("")(t.Context(), model.Output{URL: "http://vendor.test/a.png"})
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("err = %v, want an https requirement", err)
	}
}

func TestDefaultFetcherRejectsNonPublicAddresses(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://127.0.0.1/a.png",
		"https://10.1.2.3/a.png",
		"https://192.168.0.1/a.png",
		"https://172.16.0.1/a.png",
		"https://169.254.1.1/a.png",
		"https://0.0.0.0/a.png",
		"https://[::1]/a.png",
		"https://[fd00::1]/a.png",
		"https://100.64.0.1/a.png",
		"https://198.18.0.1/a.png",
		"https://192.0.2.1/a.png",
		"https://[2001:db8::1]/a.png",
	} {
		_, _, err := defaultFetcher("")(t.Context(), model.Output{URL: raw})
		if err == nil || !strings.Contains(err.Error(), "non-public") {
			t.Fatalf("%s: err = %v, want a non-public address refusal", raw, err)
		}
	}
}

// testDialer returns a dialer that connects to server's listener whatever
// address the fetcher pinned, so a test can serve the request without a
// network.
func testDialer(server *httptest.Server) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, server.Listener.Addr().String())
	}
}

func TestOutputFetcherRefusesRedirects(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://vendor.test/other.png", http.StatusFound)
	}))
	defer server.Close()
	f := outputFetcher{
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		dial:      testDialer(server),
		tlsConfig: server.Client().Transport.(*http.Transport).TLSClientConfig,
	}
	_, _, err := f.fetch(t.Context(), model.Output{URL: "https://example.com/a.png"})
	if !errors.Is(err, errRedirect) {
		t.Fatalf("err = %v, want a refused redirect", err)
	}
}

func TestOutputFetcherPinsTheResolvedAddress(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	var lookups int
	var dialed string
	f := outputFetcher{
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			lookups++
			if lookups == 1 {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			}
			// A DNS-rebinding answer: the second resolution is private.
			return []net.IPAddr{{IP: net.ParseIP("10.0.0.7")}}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = addr
			var d net.Dialer
			return d.DialContext(ctx, network, server.Listener.Addr().String())
		},
		tlsConfig: server.Client().Transport.(*http.Transport).TLSClientConfig,
	}
	data, _, err := f.fetch(t.Context(), model.Output{URL: "https://example.com/a.png"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("body = %q, want ok", data)
	}
	if lookups != 1 {
		t.Fatalf("resolved %d times, want one: a second answer could rebind the dial", lookups)
	}
	if dialed != "93.184.216.34:443" {
		t.Fatalf("dialed %q, want the pinned public address", dialed)
	}
}

func TestPublicAddressDeniesNonGlobalUnicast(t *testing.T) {
	t.Parallel()
	for raw, wantPublic := range map[string]bool{
		"93.184.216.34":   true,
		"100.128.0.1":     true, // just past carrier-grade NAT
		"198.20.0.1":      true, // just past benchmarking
		"2606:4700::1111": true,
		"100.64.0.1":      false,
		"100.127.255.254": false,
		"198.18.0.1":      false,
		"198.19.255.254":  false,
		"192.0.0.9":       false,
		"192.88.99.1":     false,
		"198.51.100.1":    false,
		"203.0.113.1":     false,
		"240.0.0.1":       false,
		"2001:db8::1":     false,
		"::1":             false,
		"fe80::1":         false,
	} {
		if got := publicAddress(net.ParseIP(raw)); got != wantPublic {
			t.Errorf("publicAddress(%s) = %v, want %v", raw, got, wantPublic)
		}
	}
}

func TestOutputPathMustStayUnderTheRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inside := filepath.Join(root, "frame.png")
	if err := os.WriteFile(inside, []byte("frame"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	data, _, err := defaultFetcher(root)(t.Context(), model.Output{Path: inside})
	if err != nil || string(data) != "frame" {
		t.Fatalf("inside root: data = %q, err = %v, want the file", data, err)
	}
	for name, output := range map[string]model.Output{
		"no root":         {Path: inside},
		"outside root":    {Path: outside},
		"symlink escape":  {Path: link},
		"relative escape": {Path: filepath.Join(root, "..", "secret.txt")},
	} {
		fetch := defaultFetcher(root)
		if name == "no root" {
			fetch = defaultFetcher("")
		}
		if _, _, err := fetch(t.Context(), output); err == nil {
			t.Fatalf("%s: path %q was read, want a refusal", name, output.Path)
		}
	}
}

func TestReadBoundedStopsAtTheLimit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "frame.png")
	if err := os.WriteFile(path, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readBounded(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 11 {
		t.Fatalf("read %d bytes, want limit+1 = 11", len(data))
	}
}

func TestIngestErrorDetailsCarryNoPresignedURL(t *testing.T) {
	t.Parallel()
	const signed = "https://vendor.test/a.png?X-Amz-Signature=secret"
	h := testHandler(t, Deps{
		Blobs: newMemBlob(),
		Fetch: func(context.Context, model.Output) ([]byte, string, error) {
			return nil, "", errors.New(`Get "` + signed + `": connection reset`)
		},
	})
	raw, err := json.Marshal(ingestIn{
		Model:  "sim-image",
		Op:     "image.generate",
		Output: []model.Output{{URL: signed, Media: "image"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.ingestStep(t.Context(), raw)
	if core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("err = %v, want vendor", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("err %q leaks the URL", err)
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T, want *core.Error", err)
	}
	if ce.Details["cause"] != "fetch_failed" || ce.Details["host"] != "vendor.test" || ce.Details["media"] != "image" {
		t.Fatalf("details = %v, want a fixed cause and non-sensitive fields", ce.Details)
	}
	for _, value := range ce.Details {
		if text, ok := value.(string); ok && strings.Contains(text, "secret") {
			t.Fatalf("details %v leak the URL", ce.Details)
		}
	}
}
