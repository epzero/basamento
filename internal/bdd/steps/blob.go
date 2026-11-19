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
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/s3sim"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/blob"
)

type blobWorld struct {
	store        *blob.Store
	altStore     *blob.Store
	blob         platform.Blob
	handler      http.Handler
	handlerSrv   *httptest.Server
	s3           *s3sim.Server
	locURL       string
	dataDir      string
	signKey      []byte
	altSignKey   []byte
	maxTTL       time.Duration
	content      map[string][]byte
	lastErr      error
	blobURL      string
	httpResp     fetchResp
	subdirRoot   string
	backend      string
	matrixID     string
	scenarioName string
	openErr      error
	lastExists   bool
	hasExists    bool
}

type fetchResp struct {
	status  int
	ctype   string
	body    []byte
	headers http.Header
}

func newBlobWorld() *blobWorld {
	return &blobWorld{
		content:    map[string][]byte{},
		signKey:    []byte("bdd-blob-signing-key-32-bytes!!!!"),
		altSignKey: []byte("other-blob-signing-key-32bytes!!"),
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
	if w.altStore != nil {
		_ = w.altStore.Close()
		w.altStore = nil
	}
	opts := blob.Options{
		Clock:     clock,
		MaxURLTTL: w.maxTTL,
		BaseURL:   "http://blob.test" + blob.HandlerRoute(),
		SignKey:   w.signKey,
	}
	useS3Backend := w.backend == "s3"
	if useS3Backend {
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
	if useS3Backend {
		if err := w.assertOpenedS3Simulator(raw); err != nil {
			_ = st.Close()
			w.store = nil
			w.blob = nil
			w.openErr = err
			return err
		}
	}
	w.store = st
	w.blob = st
	w.locURL = raw
	w.handler = h
	w.emitS3OpenMarkerIfS3(raw)
	if h != nil {
		w.handlerSrv = httptest.NewServer(h)
		altOpts := opts
		altOpts.SignKey = w.altSignKey
		altSt, _, err := blob.Open(ctx, raw, altOpts)
		if err != nil {
			return err
		}
		w.altStore = altSt
	}
	return nil
}

func registerBlob(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		w := world(ctx).Blob
		w.scenarioName = scn.Name
		var err error
		w.matrixID, err = matrixIDFromGodogScenario(scn, w.backend)
		if err != nil {
			return ctx, err
		}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w := world(ctx).Blob
		if w.s3 != nil {
			w.s3.Close()
			w.s3 = nil
		}
		if w.altStore != nil {
			_ = w.altStore.Close()
			w.altStore = nil
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
	sc.Step(`^tenant "([^"]*)" stores "([^"]*)" as "([^"]*)"$`, whenStoreString)
	sc.Step(`^tenant "([^"]*)" stores "([^"]*)" with no content type$`, func(ctx context.Context, tenant, body string) error {
		return whenPut(ctx, tenant, world(ctx).Blob.keyOf(body), strings.NewReader(body), "")
	})
	sc.Step(`^tenant "([^"]*)" has stored "([^"]*)" as "([^"]*)"$`, givenStoreString)
	sc.Step(`^tenant "([^"]*)" has stored "([^"]*)" with no content type$`, func(ctx context.Context, tenant, body string) error {
		return givenPut(ctx, tenant, blob.ContentKeyOf([]byte(body)), strings.NewReader(body), "")
	})
	sc.Step(`^tenant "([^"]*)" has deleted the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		err := world(ctx).Blob.blob.Delete(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		world(ctx).Blob.lastErr = err
		return err
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
	sc.Step(`^tenant "([^"]*)" stores content "([^"]*)" and (the source fails|the call is cancelled) after (\d+) MiB$`, func(ctx context.Context, tenant, name, kind string, mib int) error {
		limit := mib * 1024 * 1024
		b := world(ctx).Blob.content[name]
		if kind == "the source fails" {
			return tenantPut(ctx, tenant, blob.ContentKeyOf(b), &failAfterReader{b: b, limit: limit}, "application/octet-stream")
		}
		key := blob.ContentKeyOf(b)
		cctx := world(ctx).Blob.tenantCtx(ctx, tenant)
		cancelCtx, cancelFn := context.WithCancel(cctx)
		r := &cancelAfterReader{ctx: cancelCtx, b: b, limit: limit, cancel: cancelFn}
		world(ctx).Blob.lastErr = world(ctx).Blob.blob.Put(cancelCtx, key, r, "application/octet-stream")
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
		ct, err := world(ctx).Blob.store.ContentType(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		if err != nil {
			return err
		}
		if ct != want {
			return fmt.Errorf("content type %q, want %q", ct, want)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" checks the existence of the content key of "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		ok, err := world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), world(ctx).Blob.keyOf(name))
		world(ctx).Blob.lastErr = err
		world(ctx).Blob.hasExists = true
		world(ctx).Blob.lastExists = ok
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
		var err error
		if strings.HasPrefix(alt, "its tenant replaced by ") {
			tenant := strings.TrimPrefix(alt, "its tenant replaced by ")
			tenant = strings.Trim(tenant, `"`)
			raw, err = alterProxiedBlobURL(ctx, raw, alt, "", tenant)
		} else {
			other := world(ctx).Blob.keyOf("world")
			raw, err = alterProxiedBlobURL(ctx, raw, alt, other, "")
		}
		if err != nil {
			return err
		}
		return fetchRawURL(ctx, raw)
	})
	sc.Step(`^the response has status (\d+), content type "([^"]*)" and body "([^"]*)"$`, assertFetchFull)
	sc.Step(`^the response has status (\d+) and (body "[^"]*"|an empty body)$`, assertFetchStatusTail)
	sc.Step(`^the response carries these headers:$`, assertResponseHeaders)
	sc.Step(`^the response has status (\d+) and its body does not contain "([^"]*)"$`, func(ctx context.Context, status int, sub string) error {
		if world(ctx).Blob.httpResp.status != status {
			return fmt.Errorf("status %d, want %d", world(ctx).Blob.httpResp.status, status)
		}
		if strings.Contains(string(world(ctx).Blob.httpResp.body), sub) {
			return fmt.Errorf("body contains %q", sub)
		}
		return nil
	})
	// One phrase advances the scenario clock for every domain. The optional
	// "without the gate ... being resolved" clause is the flow host's way of
	// saying no person answered; the recording host keeps its own clock, so
	// advance it too when it is the one under test.
	sc.Step(`^(-?\d.*?) pass(?:es)?(?: without the gate "[^"]*" being resolved)?$`, func(ctx context.Context, elapsed string) error {
		d, err := parseElapsed(elapsed)
		if err != nil {
			return err
		}
		world(ctx).Core.clock.Advance(d)
		h := world(ctx).Host
		if h == nil || h.dc == nil {
			return nil
		}
		h.dc.advance(d)
		h.signal()
		return h.settle()
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
		w := world(ctx).Blob
		if w.lastErr != nil {
			return fmt.Errorf("unexpected error: %w", w.lastErr)
		}
		if !w.hasExists {
			return fmt.Errorf("existence not checked")
		}
		if w.lastExists {
			return fmt.Errorf("blob exists")
		}
		return nil
	})
	// opening.feature
	sc.Step(`^a blob store is opened from a file URL of a temporary directory with a signing key of (\d+) bytes$`, openBlobFileWithSignKeyLen)
	sc.Step(`^a blob store is opened from a file URL of a temporary directory without a signing key$`, openBlobFileWithoutSignKey)
	sc.Step(`^the store is open$`, func(ctx context.Context) error {
		w := world(ctx).Blob
		if w.openErr != nil {
			return w.openErr
		}
		if w.store == nil {
			return fmt.Errorf("store not open")
		}
		return nil
	})
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
		ok, err := world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), key)
		world(ctx).Blob.lastErr = err
		world(ctx).Blob.hasExists = true
		world(ctx).Blob.lastExists = ok
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

func whenStoreString(ctx context.Context, tenant, body, ctype string) error {
	return whenPut(ctx, tenant, blob.ContentKeyOf([]byte(body)), strings.NewReader(body), ctype)
}

func givenStoreString(ctx context.Context, tenant, body, ctype string) error {
	return givenPut(ctx, tenant, blob.ContentKeyOf([]byte(body)), strings.NewReader(body), ctype)
}

func putBlob(ctx context.Context, tenant, key string, r io.Reader, ctype string) error {
	w := world(ctx).Blob
	cctx := w.tenantCtx(ctx, tenant)
	if tenant == "" {
		cctx = ctx
	}
	return w.blob.Put(cctx, key, r, ctype)
}

func whenPut(ctx context.Context, tenant, key string, r io.Reader, ctype string) error {
	world(ctx).Blob.lastErr = putBlob(ctx, tenant, key, r, ctype)
	return nil
}

func givenPut(ctx context.Context, tenant, key string, r io.Reader, ctype string) error {
	err := putBlob(ctx, tenant, key, r, ctype)
	world(ctx).Blob.lastErr = err
	return err
}

func tenantPut(ctx context.Context, tenant, key string, r io.Reader, ctype string) error {
	return whenPut(ctx, tenant, key, r, ctype)
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
	d, err := parseElapsed(lifetime)
	if err != nil {
		return err
	}
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
		w.httpResp = fetchResp{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: b, headers: resp.Header.Clone()}
		return nil
	}
	resp, err := http.Get(raw)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	w.httpResp = fetchResp{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: b, headers: resp.Header.Clone()}
	return nil
}

func assertResponseHeaders(ctx context.Context, table *godog.Table) error {
	if len(table.Rows) < 2 {
		return fmt.Errorf("header table too short")
	}
	got := world(ctx).Blob.httpResp.headers
	if got == nil {
		return fmt.Errorf("no response headers recorded")
	}
	for i, row := range table.Rows[1:] {
		if len(row.Cells) < 2 {
			return fmt.Errorf("row %d: want header and value", i+1)
		}
		name := row.Cells[0].Value
		want := row.Cells[1].Value
		if have := got.Get(name); have != want {
			return fmt.Errorf("header %q: got %q want %q", name, have, want)
		}
	}
	return nil
}

func openBlobFileWithSignKeyLen(ctx context.Context, n int) error {
	dir, err := os.MkdirTemp("", "blob-open-*")
	if err != nil {
		return err
	}
	world(ctx).Blob.dataDir = dir
	return openBlobFileWithKey(ctx, dir, bytes.Repeat([]byte("k"), n))
}

func openBlobFileWithoutSignKey(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "blob-open-*")
	if err != nil {
		return err
	}
	world(ctx).Blob.dataDir = dir
	return openBlobFileWithKey(ctx, dir, nil)
}

func openBlobFileWithKey(ctx context.Context, dir string, signKey []byte) error {
	w := world(ctx).Blob
	opts := blob.Options{
		Clock:     world(ctx).Core.clock,
		MaxURLTTL: w.maxTTL,
		BaseURL:   "http://blob.test" + blob.HandlerRoute(),
		SignKey:   signKey,
	}
	raw := (&url.URL{Scheme: "file", Path: dir}).String()
	st, h, err := blob.Open(ctx, raw, opts)
	w.openErr = err
	if err != nil {
		w.store, w.blob = nil, nil
		return nil
	}
	w.store, w.blob = st, st
	w.locURL = raw
	w.signKey = append([]byte(nil), signKey...)
	w.handler = h
	w.emitS3OpenMarkerIfS3(raw)
	if h != nil {
		if w.handlerSrv != nil {
			w.handlerSrv.Close()
		}
		w.handlerSrv = httptest.NewServer(h)
	}
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
		case strings.HasPrefix(unit, "millisecond"):
			total += time.Duration(n) * time.Millisecond
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
	w := world(ctx).Blob
	opts := blob.Options{Clock: world(ctx).Core.clock, MaxURLTTL: w.maxTTL,
		BaseURL: "http://blob.test" + blob.HandlerRoute(), SignKey: w.signKey}
	st, h, err := blob.Open(ctx, raw, opts)
	w.openErr = err
	if err != nil {
		return nil
	}
	w.store = st
	w.blob = st
	w.locURL = raw
	w.handler = h
	w.emitS3OpenMarkerIfS3(raw)
	if h != nil {
		w.handlerSrv = httptest.NewServer(h)
	}
	return nil
}

func (w *blobWorld) assertOpenedS3Simulator(raw string) error {
	if w.s3 == nil {
		return fmt.Errorf("s3 conformance backend configured but simulator is nil")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "s3" || !strings.HasPrefix(raw, w.s3.URL) {
		return fmt.Errorf("s3 conformance backend configured but opened %q", raw)
	}
	return nil
}

func (w *blobWorld) emitS3OpenMarkerIfS3(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "s3" || w.matrixID == "" {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "blob-conformance backend=s3 opened=s3 id=%s scenario=%q\n",
		w.matrixID, w.scenarioName)
}

func alterProxiedBlobURL(ctx context.Context, raw, alteration, otherKey, otherTenant string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	tenant, hexd, ok := proxiedPathParts(u.Path)
	if !ok {
		return "", fmt.Errorf("bad proxied path")
	}
	sig := u.Query().Get("sig")
	expStr := u.Query().Get("exp")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimSuffix(u.Path, "/"+tenant+"/"+hexd)
	if strings.HasPrefix(alteration, "its tenant replaced by ") {
		tenant = url.PathEscape(otherTenant)
		return fmt.Sprintf("%s://%s%s/%s/%s?exp=%d&sig=%s", u.Scheme, u.Host, basePath, tenant, hexd, exp, sig), nil
	}
	switch alteration {
	case `its key replaced by the content key of "world"`:
		hexd = strings.TrimPrefix(otherKey, "sha256:")
	case "its expiry moved 1 hour later":
		exp += 3600
	case "its signature removed":
		sig = ""
	case "its signature replaced by one from another signing key":
		w := world(ctx).Blob
		if w.altStore == nil {
			return "", fmt.Errorf("alternate signing store not configured")
		}
		key := "sha256:" + hexd
		ttl := time.Unix(exp, 0).Sub(world(ctx).Core.clock.Now())
		if ttl < time.Second {
			ttl = time.Second
		}
		altURL, err := w.altStore.SignedURL(w.tenantCtx(ctx, tenant), key, ttl)
		if err != nil {
			return "", err
		}
		altU, err := url.Parse(altURL)
		if err != nil {
			return "", err
		}
		sig = altU.Query().Get("sig")
	default:
		return "", fmt.Errorf("unknown alteration %q", alteration)
	}
	out := fmt.Sprintf("%s://%s%s/%s/%s?exp=%d&sig=%s", u.Scheme, u.Host, basePath, tenant, hexd, exp, sig)
	return out, nil
}

func proxiedPathParts(p string) (tenant, hexd string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, strings.Trim(path.Clean(blob.HandlerRoute()), "/")+"/")
	parts := strings.Split(p, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
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
