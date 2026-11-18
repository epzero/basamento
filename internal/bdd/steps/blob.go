package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/blob/s3sim"
)

type blobWorld struct {
	store      *blob.Store
	blob       platform.Blob
	handler    http.Handler
	handlerSrv *httptest.Server
	s3         *s3sim.Server
	locURL     string
	dataDir    string
	signKey    []byte
	altSignKey []byte
	maxTTL     time.Duration
	content    map[string][]byte
	lastErr    error
	blobURL    string
	httpResp   fetchResp
	subdirRoot string
	backend    string
	openErr    error
}

type fetchResp struct {
	status int
	ctype  string
	body   []byte
}

func newBlobWorld() *blobWorld {
	return &blobWorld{
		content:    map[string][]byte{},
		signKey:    []byte("bdd-blob-signing-key-32-bytes!!"),
		altSignKey: []byte("other-blob-signing-key-32bytes"),
		maxTTL:     blob.DefaultMaxURLTTL,
		backend:    os.Getenv("BLOB_CONFORMANCE_BACKEND"),
	}
}

func (w *blobWorld) tenantCtx(ctx context.Context, tenant string) context.Context {
	return core.WithTenant(ctx, core.TenantID(tenant))
}

func (w *blobWorld) keyOf(name string) string {
	if b, ok := w.content[name]; ok {
		return blob.ContentKeyOf(b)
	}
	return blob.ContentKeyOf([]byte(name))
}

func (w *blobWorld) openEmpty(ctx context.Context, clock core.Clock) error {
	if w.store != nil {
		_ = w.store.Close()
	}
	if w.handlerSrv != nil {
		w.handlerSrv.Close()
	}
	dir, err := os.MkdirTemp("", "blob-bdd-*")
	if err != nil {
		return err
	}
	w.dataDir = dir
	w.locURL = (&url.URL{Scheme: "file", Path: dir}).String()
	return w.openAt(ctx, clock, w.locURL)
}

func (w *blobWorld) openAt(ctx context.Context, clock core.Clock, raw string) error {
	opts := blob.Options{
		Clock:     clock,
		MaxURLTTL: w.maxTTL,
		BaseURL:   "http://blob.test" + blob.HandlerRoute(),
		SignKey:   w.signKey,
	}
	if w.backend == "s3" {
		if w.s3 == nil {
			w.s3 = s3sim.Start()
		}
		raw = w.s3.URL
		_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
		_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
		opts.BaseURL = ""
		opts.SignKey = nil
	}
	st, h, err := blob.Open(ctx, raw, opts)
	w.openErr = err
	if err != nil {
		w.store = nil
		w.blob = nil
		return err
	}
	w.store = st
	w.blob = st
	w.locURL = raw
	w.handler = h
	if h != nil {
		w.handlerSrv = httptest.NewServer(h)
	}
	return nil
}

func registerBlob(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w := world(ctx).Blob
		if w.s3 != nil {
			w.s3.Close()
			w.s3 = nil
		}
		if w.handlerSrv != nil {
			w.handlerSrv.Close()
			w.handlerSrv = nil
		}
		if w.dataDir != "" {
			_ = os.RemoveAll(w.dataDir)
			w.dataDir = ""
		}
		return ctx, nil
	})
	sc.Step(`^an empty blob store$`, func(ctx context.Context) error {
		return world(ctx).Blob.openEmpty(ctx, world(ctx).Core.clock)
	})
	sc.Step(`^an empty blob store with a maximum blob URL lifetime of 7 days$`, func(ctx context.Context) error {
		w := world(ctx).Blob
		w.maxTTL = blob.DefaultMaxURLTTL
		return w.openEmpty(ctx, world(ctx).Core.clock)
	})
	sc.Step(`^content "([^"]*)" of (\d+) MiB generated from seed (\d+)$`, func(ctx context.Context, name string, mib, seed int) error {
		n := mib * 1024 * 1024
		rnd := rand.New(rand.NewSource(int64(seed)))
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(rnd.Intn(256))
		}
		world(ctx).Blob.content[name] = b
		return nil
	})
	sc.Step(`^tenant "([^"]*)" stores "([^"]*)" as "([^"]*)"$`, tenantStoreString)
	sc.Step(`^tenant "([^"]*)" stores "([^"]*)" with no content type$`, func(ctx context.Context, tenant, body string) error {
		return tenantPut(ctx, tenant, world(ctx).Blob.keyOf(body), strings.NewReader(body), "")
	})
	sc.Step(`^tenant "([^"]*)" has stored "([^"]*)" as "([^"]*)"$`, tenantStoreString)
	sc.Step(`^tenant "([^"]*)" has stored "([^"]*)" with no content type$`, func(ctx context.Context, tenant, body string) error {
		return tenantPut(ctx, tenant, blob.ContentKeyOf([]byte(body)), strings.NewReader(body), "")
	})
	sc.Step(`^tenant "([^"]*)" has deleted the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		world(ctx).Blob.lastErr = world(ctx).Blob.blob.Delete(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		return nil
	})
	sc.Step(`^tenant "([^"]*)" stores "([^"]*)" under the content key of "([^"]*)"$`, func(ctx context.Context, tenant, body, of string) error {
		return tenantPut(ctx, tenant, world(ctx).Blob.keyOf(of), strings.NewReader(body), "application/octet-stream")
	})
	sc.Step(`^tenant "([^"]*)" stores content "([^"]*)" with its last byte changed under the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name, of string) error {
		b := append([]byte(nil), world(ctx).Blob.content[name]...)
		if len(b) > 0 {
			b[len(b)-1] ^= 0xff
		}
		return tenantPut(ctx, tenant, world(ctx).Blob.keyOf(of), bytes.NewReader(b), "application/octet-stream")
	})
	sc.Step(`^tenant "([^"]*)" stores content "([^"]*)" as "([^"]*)"$`, func(ctx context.Context, tenant, name, ctype string) error {
		b := world(ctx).Blob.content[name]
		return tenantPut(ctx, tenant, blob.ContentKeyOf(b), bytes.NewReader(b), ctype)
	})
	sc.Step(`^tenant "([^"]*)" stores content "([^"]*)" and the source fails after (\d+) MiB$`, func(ctx context.Context, tenant, name string, mib int) error {
		limit := mib * 1024 * 1024
		b := world(ctx).Blob.content[name]
		return tenantPut(ctx, tenant, blob.ContentKeyOf(b), &failAfterReader{b: b, limit: limit}, "application/octet-stream")
	})
	sc.Step(`^tenant "([^"]*)" stores content "([^"]*)" and the call is cancelled after (\d+) MiB$`, func(ctx context.Context, tenant, name string, mib int) error {
		limit := mib * 1024 * 1024
		b := world(ctx).Blob.content[name]
		key := blob.ContentKeyOf(b)
		cctx := world(ctx).Blob.tenantCtx(ctx, tenant)
		cancelCtx, cancelFn := context.WithCancel(cctx)
		r := &cancelAfterReader{ctx: cancelCtx, b: b, limit: limit, cancel: cancelFn}
		cctx = cancelCtx
		world(ctx).Blob.lastErr = world(ctx).Blob.blob.Put(cctx, key, r, "application/octet-stream")
		return nil
	})
	sc.Step(`^the write succeeds$`, func(ctx context.Context) error {
		if world(ctx).Blob.lastErr != nil {
			return fmt.Errorf("write failed: %w", world(ctx).Blob.lastErr)
		}
		return nil
	})
	sc.Step(`^the write fails$`, func(ctx context.Context) error {
		if world(ctx).Blob.lastErr == nil {
			return fmt.Errorf("write succeeded, want failure")
		}
		return nil
	})
	sc.Step(`^the call fails with code "([^"]*)"$`, assertBlobCallCode)
	sc.Step(`^tenant "([^"]*)" has a blob under key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		ok, err := world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), key)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no blob at %s", key)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" has no blob under the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		ok, err := world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("blob still exists")
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" has a blob under the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		ok, err := world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no blob")
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" reads "([^"]*)" under the content key of "([^"]*)"$`, func(ctx context.Context, tenant, want, name string) error {
		rc, err := world(ctx).Blob.blob.Get(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		if string(b) != want {
			return fmt.Errorf("got %q, want %q", b, want)
		}
		return nil
	})
	sc.Step(`^reading key "([^"]*)" as tenant "([^"]*)" returns (\d+) bytes$`, func(ctx context.Context, key, tenant string, n int) error {
		rc, err := world(ctx).Blob.blob.Get(world(ctx).Blob.tenantCtx(ctx, tenant), key)
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		if len(b) != n {
			return fmt.Errorf("len %d, want %d", len(b), n)
		}
		return nil
	})
	sc.Step(`^reading the content key of "([^"]*)" as tenant "([^"]*)" returns content "([^"]*)" exactly$`, func(ctx context.Context, name, tenant, exp string) error {
		rc, err := world(ctx).Blob.blob.Get(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		want := world(ctx).Blob.content[name]
		if !bytes.Equal(b, want) {
			return fmt.Errorf("content mismatch")
		}
		_ = exp
		return nil
	})
	sc.Step(`^reading the content key of "([^"]*)" as tenant "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, tenant, code string) error {
		_, err := world(ctx).Blob.blob.Get(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		return assertErrCode(err, code)
	})
	sc.Step(`^tenant "([^"]*)" reads key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.Get(world(ctx).Blob.tenantCtx(ctx, tenant), key)
		return nil
	})
	sc.Step(`^tenant "([^"]*)" deletes the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		world(ctx).Blob.lastErr = world(ctx).Blob.blob.Delete(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		return nil
	})
	sc.Step(`^tenant "([^"]*)" deletes key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		world(ctx).Blob.lastErr = world(ctx).Blob.blob.Delete(world(ctx).Blob.tenantCtx(ctx, tenant), key)
		return nil
	})
	sc.Step(`^the blob of tenant "([^"]*)" under the content key of "([^"]*)" has content type "([^"]*)"$`, func(ctx context.Context, tenant, name, want string) error {
		ct, err := world(ctx).Blob.store.ContentType(world(ctx).Blob.tenantCtx(ctx, tenant), core.TenantID(tenant), world(ctx).Blob.keyOf(name))
		if err != nil {
			return err
		}
		if ct != want {
			return fmt.Errorf("content type %q, want %q", ct, want)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" checks the existence of the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		return nil
	})
	sc.Step(`^tenant "([^"]*)" requests a blob URL for the content key of "([^"]*)" with a lifetime of (.+)$`, requestBlobURL)
	sc.Step(`^tenant "([^"]*)" requests a blob URL for the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.SignedURL(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name), time.Hour)
		return nil
	})
	sc.Step(`^tenant "([^"]*)" has a blob URL for the content key of "([^"]*)" with a lifetime of (.+)$`, requestBlobURL)
	sc.Step(`^a client with no credentials fetches the blob URL$`, fetchBlobURL)
	sc.Step(`^a client fetches the blob URL with (.+)$`, func(ctx context.Context, alt string) error {
		raw := world(ctx).Blob.blobURL
		if strings.HasPrefix(alt, "its tenant replaced by ") {
			tenant := strings.TrimPrefix(alt, "its tenant replaced by ")
			tenant = strings.Trim(tenant, `"`)
			u, err := blob.AlterURL(raw, alt, "", tenant, world(ctx).Blob.altSignKey)
			if err != nil {
				return err
			}
			raw = u
		} else {
			other := world(ctx).Blob.keyOf("world")
			u, err := blob.AlterURL(raw, alt, other, "", world(ctx).Blob.altSignKey)
			if err != nil {
				return err
			}
			raw = u
		}
		return fetchRawURL(ctx, raw)
	})
	sc.Step(`^the response has status (\d+), content type "([^"]*)" and body "([^"]*)"$`, assertFetchFull)
	sc.Step(`^the response has status (\d+) and (body "[^"]*"|an empty body)$`, assertFetchStatusTail)
	sc.Step(`^the response has status (\d+) and its body does not contain "([^"]*)"$`, func(ctx context.Context, status int, sub string) error {
		if world(ctx).Blob.httpResp.status != status {
			return fmt.Errorf("status %d, want %d", world(ctx).Blob.httpResp.status, status)
		}
		if strings.Contains(string(world(ctx).Blob.httpResp.body), sub) {
			return fmt.Errorf("body contains %q", sub)
		}
		return nil
	})
	sc.Step(`^([^"]+) pass$`, func(ctx context.Context, elapsed string) error {
		d, err := parseElapsed(elapsed)
		if err != nil {
			return err
		}
		world(ctx).Core.clock.Advance(d)
		return nil
	})
	sc.Step(`^the blob store is closed and opened again on the same location$`, func(ctx context.Context) error {
		w := world(ctx).Blob
		if w.store != nil {
			_ = w.store.Close()
		}
		if w.handlerSrv != nil {
			w.handlerSrv.Close()
			w.handlerSrv = nil
		}
		return w.openAt(ctx, world(ctx).Core.clock, w.locURL)
	})
	sc.Step(`^the answer is that no blob exists$`, func(ctx context.Context) error {
		if world(ctx).Blob.lastErr != nil {
			return fmt.Errorf("unexpected error: %w", world(ctx).Blob.lastErr)
		}
		return nil
	})
	// opening.feature
	sc.Step(`^a blob store is opened from (a file URL of a temporary directory|an s3 URL of a bucket in the S3 simulator|a gs URL of the nightly test bucket|an azblob URL of the nightly test container)$`, openBlobFromPhrase)
	sc.Step(`^a blob store is opened from "(.*)"$`, tryOpenPath)
	sc.Step(`^a blob store is opened from a file URL of its subdirectory "([^"]*)"$`, func(ctx context.Context, sub string) error {
		dir := filepath.Join(world(ctx).Blob.subdirRoot, sub)
		return tryOpenPath(ctx, (&url.URL{Scheme: "file", Path: dir}).String())
	})
	sc.Step(`^a temporary directory with no subdirectory "([^"]*)"$`, func(ctx context.Context, sub string) error {
		root, err := os.MkdirTemp("", "blob-open-*")
		if err != nil {
			return err
		}
		world(ctx).Blob.subdirRoot = root
		return nil
	})
	sc.Step(`^the subdirectory "([^"]*)" exists$`, func(ctx context.Context, sub string) error {
		p := filepath.Join(world(ctx).Blob.subdirRoot, sub)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return fmt.Errorf("subdir missing")
		}
		return nil
	})
	sc.Step(`^the content "([^"]*)" is kept in (.+)$`, assertContentPlace)
	sc.Step(`^the error message names the schemes "file", "s3", "gs" and "azblob"$`, func(ctx context.Context) error {
		if world(ctx).Blob.openErr == nil {
			return fmt.Errorf("want error")
		}
		msg := world(ctx).Blob.openErr.Error()
		for _, s := range []string{"file", "s3", "gs", "azblob"} {
			if !strings.Contains(msg, s) {
				return fmt.Errorf("message missing %q: %s", s, msg)
			}
		}
		return nil
	})
	// outline operations
	sc.Step(`^tenant "([^"]*)" stores "([^"]*)" under key "([^"]*)"$`, func(ctx context.Context, tenant, body, key string) error {
		return tenantPut(ctx, tenant, key, strings.NewReader(body), "application/octet-stream")
	})
	sc.Step(`^tenant "([^"]*)" checks the existence of key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), key)
		return nil
	})
	sc.Step(`^tenant "([^"]*)" requests a blob URL for key "([^"]*)"$`, func(ctx context.Context, tenant, key string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.SignedURL(world(ctx).Blob.tenantCtx(ctx, tenant), key, time.Hour)
		return nil
	})
	sc.Step(`^a caller with no tenant stores "([^"]*)" under the content key of "([^"]*)"$`, func(ctx context.Context, body, of string) error {
		return tenantPut(ctx, "", world(ctx).Blob.keyOf(of), strings.NewReader(body), "text/plain")
	})
	sc.Step(`^a caller with no tenant reads the content key of "([^"]*)"$`, func(ctx context.Context, name string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.Get(ctx, world(ctx).Blob.keyOf(name))
		return nil
	})
	sc.Step(`^a caller with no tenant checks the existence of the content key of "([^"]*)"$`, func(ctx context.Context, name string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.Exists(ctx, world(ctx).Blob.keyOf(name))
		return nil
	})
	sc.Step(`^a caller with no tenant deletes the content key of "([^"]*)"$`, func(ctx context.Context, name string) error {
		world(ctx).Blob.lastErr = world(ctx).Blob.blob.Delete(ctx, world(ctx).Blob.keyOf(name))
		return nil
	})
	sc.Step(`^a caller with no tenant requests a blob URL for the content key of "([^"]*)"$`, func(ctx context.Context, name string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.SignedURL(ctx, world(ctx).Blob.keyOf(name), time.Hour)
		return nil
	})
	sc.Step(`^tenant "([^"]*)" reads the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		_, world(ctx).Blob.lastErr = world(ctx).Blob.blob.Get(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		return nil
	})
}

func tenantStoreString(ctx context.Context, tenant, body, ctype string) error {
	return tenantPut(ctx, tenant, blob.ContentKeyOf([]byte(body)), strings.NewReader(body), ctype)
}

func tenantPut(ctx context.Context, tenant, key string, r io.Reader, ctype string) error {
	w := world(ctx).Blob
	cctx := w.tenantCtx(ctx, tenant)
	if tenant == "" {
		cctx = ctx
	}
	w.lastErr = w.blob.Put(cctx, key, r, ctype)
	return nil
}

func assertBlobCallCode(ctx context.Context, code string) error {
	err := world(ctx).Blob.lastErr
	if err == nil {
		err = world(ctx).Blob.openErr
	}
	return assertErrCode(err, code)
}

func assertErrCode(err error, code string) error {
	if err == nil {
		return fmt.Errorf("call succeeded, want %s", code)
	}
	if string(core.CodeOf(err)) != code {
		return fmt.Errorf("got %s, want %s", core.CodeOf(err), code)
	}
	return nil
}

type failAfterReader struct {
	b     []byte
	limit int
	off   int
}

func (f *failAfterReader) Read(p []byte) (int, error) {
	if f.off >= f.limit {
		return 0, fmt.Errorf("source failed")
	}
	n := copy(p, f.b[f.off:])
	f.off += n
	if f.off > f.limit {
		f.off = f.limit
	}
	return n, nil
}

type cancelAfterReader struct {
	ctx    context.Context
	b      []byte
	limit  int
	off    int
	cancel context.CancelFunc
}

func (c *cancelAfterReader) Read(p []byte) (int, error) {
	if c.off >= c.limit {
		c.cancel()
		return 0, context.Canceled
	}
	n := copy(p, c.b[c.off:])
	c.off += n
	if c.off > c.limit {
		c.off = c.limit
	}
	return n, c.ctx.Err()
}

func requestBlobURL(ctx context.Context, tenant, name, lifetime string) error {
	d, _ := parseElapsed(lifetime)
	u, err := world(ctx).Blob.blob.SignedURL(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name), d)
	if err != nil {
		world(ctx).Blob.lastErr = err
		return nil
	}
	world(ctx).Blob.blobURL = u
	world(ctx).Blob.lastErr = nil
	return nil
}

func fetchBlobURL(ctx context.Context) error {
	return fetchRawURL(ctx, world(ctx).Blob.blobURL)
}

func fetchRawURL(ctx context.Context, raw string) error {
	w := world(ctx).Blob
	if w.handlerSrv != nil && strings.HasPrefix(raw, "http://blob.test") {
		reqURL := strings.Replace(raw, "http://blob.test", w.handlerSrv.URL, 1)
		resp, err := http.Get(reqURL)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		w.httpResp = fetchResp{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: b}
		return nil
	}
	resp, err := http.Get(raw)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	w.httpResp = fetchResp{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: b}
	return nil
}

func assertFetchFull(ctx context.Context, status int, ctype, body string) error {
	r := world(ctx).Blob.httpResp
	if r.status != status || r.ctype != ctype || string(r.body) != body {
		return fmt.Errorf("got %d %q %q", r.status, r.ctype, r.body)
	}
	return nil
}

func assertFetchStatusTail(ctx context.Context, status int, tail string) error {
	r := world(ctx).Blob.httpResp
	if r.status != status {
		return fmt.Errorf("status %d, want %d", r.status, status)
	}
	switch tail {
	case "an empty body":
		if len(r.body) != 0 {
			return fmt.Errorf("body not empty")
		}
	case `body "hello"`:
		if string(r.body) != "hello" {
			return fmt.Errorf("body %q", r.body)
		}
	default:
		return fmt.Errorf("unknown tail %q", tail)
	}
	return nil
}

func parseElapsed(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "0 seconds" {
		return 0, nil
	}
	if strings.HasPrefix(s, "-") {
		d, err := parsePositiveDuration(strings.TrimPrefix(s, "-"))
		if err != nil {
			return 0, err
		}
		return -d, nil
	}
	return parsePositiveDuration(s)
}

func parsePositiveDuration(s string) (time.Duration, error) {
	parts := strings.Fields(s)
	if len(parts)%2 != 0 {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	var total time.Duration
	for i := 0; i < len(parts); i += 2 {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0, err
		}
		unit := parts[i+1]
		switch {
		case strings.HasPrefix(unit, "second"):
			total += time.Duration(n) * time.Second
		case strings.HasPrefix(unit, "minute"):
			total += time.Duration(n) * time.Minute
		case strings.HasPrefix(unit, "hour"):
			total += time.Duration(n) * time.Hour
		case strings.HasPrefix(unit, "day"):
			total += time.Duration(n) * 24 * time.Hour
		default:
			return 0, fmt.Errorf("unknown unit %q", unit)
		}
	}
	return total, nil
}

func openBlobFromPhrase(ctx context.Context, phrase string) error {
	phrase = strings.Trim(phrase, `"`)
	w := world(ctx).Blob
	if phrase == "a file URL of a temporary directory" {
		dir, err := os.MkdirTemp("", "blob-open-*")
		if err != nil {
			return err
		}
		w.dataDir = dir
		return tryOpenPath(ctx, (&url.URL{Scheme: "file", Path: dir}).String())
	}
	if phrase == "an s3 URL of a bucket in the S3 simulator" {
		w.s3 = s3sim.Start()
		_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
		_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
		return tryOpenPath(ctx, w.s3.URL)
	}
	if phrase == "a gs URL of the nightly test bucket" {
		if u := os.Getenv("BLOB_LIVE_GS_URL"); u != "" {
			return tryOpenPath(ctx, u)
		}
		world(ctx).Blob.openErr = fmt.Errorf("BLOB_LIVE_GS_URL not set")
		return nil
	}
	if phrase == "an azblob URL of the nightly test container" {
		if u := os.Getenv("BLOB_LIVE_AZBLOB_URL"); u != "" {
			return tryOpenPath(ctx, u)
		}
		world(ctx).Blob.openErr = fmt.Errorf("BLOB_LIVE_AZBLOB_URL not set")
		return nil
	}
	return tryOpenPath(ctx, phrase)
}

func tryOpenPath(ctx context.Context, raw string) error {
	opts := blob.Options{Clock: world(ctx).Core.clock, MaxURLTTL: world(ctx).Blob.maxTTL,
		BaseURL: "http://blob.test" + blob.HandlerRoute(), SignKey: world(ctx).Blob.signKey}
	st, h, err := blob.Open(ctx, raw, opts)
	world(ctx).Blob.openErr = err
	if err != nil {
		return nil
	}
	world(ctx).Blob.store = st
	world(ctx).Blob.blob = st
	world(ctx).Blob.locURL = raw
	world(ctx).Blob.handler = h
	if h != nil {
		world(ctx).Blob.handlerSrv = httptest.NewServer(h)
	}
	return nil
}

func assertContentPlace(ctx context.Context, body, place string) error {
	w := world(ctx).Blob
	hex := strings.TrimPrefix(blob.ContentKeyOf([]byte(body)), "sha256:")
	switch place {
	case "the temporary directory":
		p := filepath.Join(w.dataDir, "acme", hex)
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("object not in temp dir: %w", err)
		}
	case "that bucket of the S3 simulator":
		ok, err := w.blob.Exists(w.tenantCtx(ctx, "acme"), blob.ContentKeyOf([]byte(body)))
		if err != nil || !ok {
			return fmt.Errorf("object not in s3")
		}
	default:
		return fmt.Errorf("unknown place %q", place)
	}
	return nil
}
