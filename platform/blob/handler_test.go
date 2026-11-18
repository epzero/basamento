package blob

import (
	"context"
	"errors"
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

func TestHandlerNosniffAndContentLength(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, h, err := Open(context.Background(), (&url.URL{Scheme: "file", Path: dir}).String(), Options{
		Clock:   coretest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)),
		SignKey: []byte("signing-key-for-tests-only!!"),
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
}

func TestHandlerRejectsRequestWithoutSignKey(t *testing.T) {
	t.Parallel()
	hex := strings.TrimPrefix(ContentKeyOf([]byte("hello")), "sha256:")
	exp := time.Now().Add(time.Hour).Unix()
	sig := signRead(nil, "acme", hex, exp)
	h := &URLHandler{Store: &Store{}}
	req := httptest.NewRequest(http.MethodGet, "/acme/"+hex+"?exp="+strconv.FormatInt(exp, 10)+"&sig="+sig, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
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
		h := &URLHandler{Store: &Store{clock: core.SystemClock{}}}
		h.respondBody(rec, req, &failReader{n: 1}, 10, "text/plain")
	}()
	if !panicked {
		t.Fatal("want ErrAbortHandler panic")
	}
}
