package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

type failReader struct {
	n int
}

func (f *failReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("read failed")
	}
	f.n--
	if f.n == 0 {
		return 1, errors.New("read failed")
	}
	p[0] = 'x'
	return 1, nil
}

func (f *failReader) Close() error { return nil }

const testSignKey = "signing-key-for-tests-only-32b!!!!!"

func TestHandlerNosniffAndContentLength(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, h, err := Open(context.Background(), (&url.URL{Scheme: "file", Path: dir}).String(), Options{
		Clock:   coretest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)),
		SignKey: []byte(testSignKey),
		BaseURL: "http://blob.test" + HandlerRoute(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello"))
	if err := st.Put(ctx, key, strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	u, err := st.SignedURL(ctx, key, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	u = strings.Replace(u, "http://blob.test", srv.URL, 1)
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff %q", resp.Header.Get("X-Content-Type-Options"))
	}
	if resp.Header.Get("Content-Length") != "5" {
		t.Fatalf("length %q", resp.Header.Get("Content-Length"))
	}
	if resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache %q", resp.Header.Get("Cache-Control"))
	}
}

var testProxiedURLExp = time.Date(2026, time.November, 16, 15, 15, 0, 0, time.UTC).Unix()

func TestHandlerRejectsRequestWithoutSignKey(t *testing.T) {
	t.Parallel()
	hex := strings.TrimPrefix(ContentKeyOf([]byte("hello")), "sha256:")
	exp := testProxiedURLExp
	sig := signRead(nil, "acme", hex, exp)
	h := &URLHandler{Store: &Store{}}
	req := httptest.NewRequest(http.MethodGet, "/acme/"+hex+"?exp="+strconv.FormatInt(exp, 10)+"&sig="+sig, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache on error %q", rec.Header().Get("Cache-Control"))
	}
}

func TestHandlerCopyFailureAborts(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = errors.Is(r.(error), http.ErrAbortHandler)
			}
		}()
		h := &URLHandler{Store: &Store{clock: coretest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))}}
		h.respondBody(rec, req, &failReader{n: 1}, 10, "text/plain", strings.Repeat("a", 64))
	}()
	if !panicked {
		t.Fatal("want ErrAbortHandler panic")
	}
}

func TestHandlerSecureHeadersHTMLAndSVG(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := coretest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))
	st, h, err := Open(context.Background(), (&url.URL{Scheme: "file", Path: dir}).String(), Options{
		Clock: clk, SignKey: []byte(testSignKey), BaseURL: "http://blob.test" + HandlerRoute(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	for _, tc := range []struct {
		ctype string
		body  string
	}{
		{"text/html", "<script>x</script>"},
		{"image/svg+xml", "<svg xmlns=\"http://www.w3.org/2000/svg\"/>"},
	} {
		t.Run(tc.ctype, func(t *testing.T) {
			key := ContentKeyOf([]byte(tc.body))
			if err := st.Put(ctx, key, strings.NewReader(tc.body), tc.ctype); err != nil {
				t.Fatal(err)
			}
			raw, err := st.SignedURL(ctx, key, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(h)
			defer srv.Close()
			raw = strings.Replace(raw, "http://blob.test", srv.URL, 1)
			hex := strings.TrimPrefix(key, "sha256:")
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				req, err := http.NewRequest(method, raw, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				func() {
					defer func() { _ = resp.Body.Close() }()
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("%s status %d", method, resp.StatusCode)
					}
					assertProxiedOKHeaders(t, resp, hex, tc.ctype)
				}()
			}
		})
	}
}

func assertProxiedOKHeaders(t *testing.T, resp *http.Response, hex, wantType string) {
	t.Helper()
	if resp.Header.Get("Content-Type") != wantType {
		t.Fatalf("ctype %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff")
	}
	if resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache")
	}
	wantDisp := `attachment; filename="` + hex + `"`
	if resp.Header.Get("Content-Disposition") != wantDisp {
		t.Fatalf("disposition %q", resp.Header.Get("Content-Disposition"))
	}
	if resp.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Fatalf("csp %q", resp.Header.Get("Content-Security-Policy"))
	}
}

func TestProxiedTenantPathEscapeRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := coretest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))
	st, h, err := Open(context.Background(), (&url.URL{Scheme: "file", Path: dir}).String(), Options{
		Clock: clk, SignKey: []byte(testSignKey), BaseURL: "http://blob.test" + HandlerRoute(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	tenant := "a%2Eb"
	ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
	body := []byte("tenant-bytes")
	key := ContentKeyOf(body)
	if err := st.Put(ctx, key, strings.NewReader(string(body)), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	raw, err := st.SignedURL(ctx, key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	pu, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	testURL := srv.URL + pu.EscapedPath() + "?" + pu.RawQuery
	resp, err := http.Get(testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("body %q", got)
	}
}
