package steps

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/blob"
)

type commitResult struct {
	key string
	err error
}

type blobUploadState struct {
	tenant     string
	uploads    []platform.Upload
	commits    []commitResult
	lastUpload *platform.Upload
}

func (w *blobWorld) uploadState() *blobUploadState {
	if w.uploadSt == nil {
		w.uploadSt = &blobUploadState{}
	}
	return w.uploadSt
}

func registerBlobUploads(sc *godog.ScenarioContext) {
	sc.Step(`^content "([^"]*)" of (\d+) MiB and 1 byte generated from seed (\d+)$`, sizedContentPlusOne)
	sc.Step(`^tenant "([^"]*)" starts an upload of at most (.+) as "([^"]*)" with a lifetime of (.+)$`, startUpload)
	sc.Step(`^tenant "([^"]*)" starts two uploads of at most (.+) as "([^"]*)" with a lifetime of (.+), (.+)$`, startTwoUploads)
	sc.Step(`^tenant "([^"]*)" starts an upload with (.+)$`, startUploadBadOpts)
	sc.Step(`^a caller with no tenant starts an upload of at most (.+) as "([^"]*)" with a lifetime of (.+)$`, startUploadNoTenant)
	sc.Step(`^a client with no credentials uploads "([^"]*)" as "([^"]*)" to the upload URL$`, clientUploadString)
	sc.Step(`^a client with no credentials uploads content "([^"]*)" as "([^"]*)" to the upload URL$`, clientUploadNamed)
	sc.Step(`^a client with no credentials uploads "([^"]*)" as "([^"]*)" to the upload URL with (.+)$`, clientUploadAltered)
	sc.Step(`^tenant "([^"]*)" commits the staging key of the upload$`, commitUpload)
	sc.Step(`^tenant "([^"]*)" commits the staging key of the second upload$`, commitSecondUpload)
	sc.Step(`^tenant "([^"]*)" commits the staging key of the upload again$`, commitUpload)
	sc.Step(`^tenant "([^"]*)" commits key "([^"]*)"$`, commitKey)
	sc.Step(`^tenant "([^"]*)" commits the staging key "([^"]*)"$`, commitStagingKeyLiteral)
	sc.Step(`^a client with no credentials uploads "([^"]*)" as "([^"]*)" to the second upload URL$`, clientUploadSecond)
	sc.Step(`^tenant "([^"]*)" has no staged object under the staging key of the first upload$`, assertNoStagedFirst)
	sc.Step(`^tenant "([^"]*)" has no blob under the content key of content "([^"]*)"$`, assertNoBlobNamedContent)
	sc.Step(`^a caller with no tenant commits the staging key of the upload$`, commitUploadNoTenant)
	sc.Step(`^the staging keys of the uploads differ$`, stagingKeysDiffer)
	sc.Step(`^the upload URLs of the uploads differ$`, uploadURLsDiffer)
	sc.Step(`^the commit returns the content key of "([^"]*)"$`, assertCommitKey)
	sc.Step(`^the commit returns the content key of content "([^"]*)"$`, assertCommitKeyNamed)
	sc.Step(`^the first commit fails with code "([^"]*)"$`, assertFirstCommitCode)
	sc.Step(`^the second commit fails with code "([^"]*)"$`, assertSecondCommitCode)
	sc.Step(`^the second commit returns the content key of "([^"]*)"$`, assertSecondCommitKey)
	sc.Step(`^the staging key of the upload differs from the content key of "([^"]*)"$`, stagingDiffersFromContent)
	sc.Step(`^the upload URL names no content key$`, uploadURLNamesNoContentKey)
	sc.Step(`^the staged object of the upload is "([^"]*)" as "([^"]*)"$`, writeStagedString)
	sc.Step(`^the staged object of the upload is content "([^"]*)"$`, writeStagedContent)
	sc.Step(`^tenant "([^"]*)" has a staged object of content "([^"]*)" as "([^"]*)" under the staging key of the upload$`, assertStagedNamed)
	sc.Step(`^tenant "([^"]*)" has a staged object of content "([^"]*)" under the staging key of the upload$`, assertStagedString)
	sc.Step(`^tenant "([^"]*)" has no staged object under the staging key of the upload$`, assertNoStaged)
	sc.Step(`^tenant "([^"]*)" has no staged object under the staging key of the second upload$`, assertNoStagedSecond)
	sc.Step(`^the expired staging slots are swept$`, sweepStaging)
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "MiB") {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "MiB")))
		if err != nil {
			return 0, err
		}
		return int64(n) * 1024 * 1024, nil
	}
	if strings.HasSuffix(s, "bytes") {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "bytes")))
		return int64(n), err
	}
	return 0, fmt.Errorf("unknown size %q", s)
}

func startUpload(ctx context.Context, tenant, maxSz, ctype, lifetime string) error {
	max, err := parseSize(maxSz)
	if err != nil {
		return err
	}
	ttl, err := parseElapsed(lifetime)
	if err != nil {
		return err
	}
	doStartUpload(ctx, tenant, platform.UploadOptions{MaxSize: max, ContentType: ctype, TTL: ttl})
	return nil
}

func startTwoUploads(ctx context.Context, tenant, maxSz, ctype, lifetime, apart string) error {
	if err := startUpload(ctx, tenant, maxSz, ctype, lifetime); err != nil {
		return err
	}
	if apart == "a minute apart" {
		world(ctx).Core.clock.Advance(time.Minute)
	}
	if err := startUpload(ctx, tenant, maxSz, ctype, lifetime); err != nil {
		return err
	}
	return nil
}

func startUploadBadOpts(ctx context.Context, tenant, opts string) error {
	o, err := parseUploadOpts(opts)
	if err != nil {
		return err
	}
	doStartUpload(ctx, tenant, o)
	return nil
}

func parseUploadOpts(opts string) (platform.UploadOptions, error) {
	o := platform.UploadOptions{TTL: 15 * time.Minute, MaxSize: 1024 * 1024, ContentType: "text/plain"}
	switch strings.TrimSpace(opts) {
	case "a maximum size of 0 bytes":
		o.MaxSize = 0
	case "a maximum size of -1 bytes":
		o.MaxSize = -1
	case "no content type":
		o.ContentType = ""
	case `content type "text"`:
		o.ContentType = "text"
	case `content type "text/plain, text/markdown"`:
		o.ContentType = "text/plain, text/markdown"
	case `content type "text/*"`:
		o.ContentType = "text/*"
	default:
		return o, fmt.Errorf("unknown options %q", opts)
	}
	return o, nil
}

func startUploadNoTenant(ctx context.Context, maxSz, ctype, lifetime string) error {
	max, err := parseSize(maxSz)
	if err != nil {
		return err
	}
	ttl, err := parseElapsed(lifetime)
	if err != nil {
		return err
	}
	world(ctx).Blob.lastErr = doStartUploadErr(ctx, "", platform.UploadOptions{MaxSize: max, ContentType: ctype, TTL: ttl})
	return nil
}

func doStartUpload(ctx context.Context, tenant string, o platform.UploadOptions) {
	world(ctx).Blob.lastErr = doStartUploadErr(ctx, tenant, o)
}

func doStartUploadErr(ctx context.Context, tenant string, o platform.UploadOptions) error {
	w := world(ctx).Blob
	cctx := w.tenantCtx(ctx, tenant)
	if tenant == "" {
		cctx = ctx
	}
	u, err := w.blob.StartUpload(cctx, o)
	if err != nil {
		return err
	}
	st := w.uploadState()
	st.tenant = tenant
	st.uploads = append(st.uploads, u)
	st.lastUpload = &st.uploads[len(st.uploads)-1]
	return nil
}

// primaryUpload is the first StartUpload in the scenario ("the upload").
func primaryUpload(w *blobWorld) platform.Upload {
	st := w.uploadState()
	if len(st.uploads) == 0 {
		return platform.Upload{}
	}
	return st.uploads[0]
}

func clientUploadString(ctx context.Context, body, ctype string) error {
	return clientUpload(ctx, []byte(body), ctype, primaryUpload(world(ctx).Blob).URL, nil)
}

func clientUploadSecond(ctx context.Context, body, ctype string) error {
	u := world(ctx).Blob.uploadState().uploads
	if len(u) < 2 {
		return fmt.Errorf("need two uploads")
	}
	return clientUpload(ctx, []byte(body), ctype, u[1].URL, nil)
}

func commitStagingKeyLiteral(ctx context.Context, tenant, stagingKey string) error {
	return commitStaging(ctx, tenant, stagingKey)
}

func assertNoStagedFirst(ctx context.Context, tenant string) error {
	u := world(ctx).Blob.uploadState().uploads
	if len(u) < 1 {
		return fmt.Errorf("need an upload")
	}
	w := world(ctx).Blob
	ok, err := w.store.StagedObjectExists(w.tenantCtx(ctx, tenant), core.TenantID(tenant), u[0].StagingKey)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("staged object still exists")
	}
	return nil
}

func assertNoBlobNamedContent(ctx context.Context, tenant, name string) error {
	b := world(ctx).Blob.content[name]
	key := blob.ContentKeyOf(b)
	ok, err := world(ctx).Blob.blob.Exists(world(ctx).Blob.tenantCtx(ctx, tenant), key)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("blob still exists")
	}
	return nil
}

func clientUploadNamed(ctx context.Context, name, ctype string) error {
	b := world(ctx).Blob.content[name]
	return clientUpload(ctx, b, ctype, primaryUpload(world(ctx).Blob).URL, nil)
}

func clientUploadAltered(ctx context.Context, body, ctype, alteration string) error {
	raw := primaryUpload(world(ctx).Blob).URL
	var err error
	switch {
	case strings.HasPrefix(alteration, "its staging key replaced by the staging key of the second upload"):
		u2 := world(ctx).Blob.uploadState().uploads
		if len(u2) < 2 {
			return fmt.Errorf("need two uploads")
		}
		raw, err = replaceUploadStagingKey(raw, u2[1].StagingKey, world(ctx).Blob.signKey)
	case strings.HasPrefix(alteration, "its tenant replaced by "):
		tenant := strings.Trim(strings.TrimPrefix(alteration, "its tenant replaced by "), `"`)
		raw, err = replaceUploadTenant(raw, tenant, world(ctx).Blob.signKey)
	case alteration == "its expiry moved 1 hour later":
		raw, err = alterUploadParam(raw, "exp", func(v string) string {
			n, _ := strconv.ParseInt(v, 10, 64)
			return strconv.FormatInt(n+3600, 10)
		}, world(ctx).Blob.signKey)
	case alteration == "its signature removed":
		raw, err = removeUploadSig(raw)
	case alteration == "its signature replaced by one from another signing key":
		raw, err = resignUpload(raw, world(ctx).Blob.altSignKey)
	case alteration == "its maximum size replaced by 2 MiB":
		raw, err = alterUploadParam(raw, "max", func(string) string { return strconv.FormatInt(2*1024*1024, 10) }, world(ctx).Blob.signKey)
	case alteration == `its content type replaced by "text/markdown"`:
		raw, err = alterUploadParam(raw, "ct", func(string) string { return url.QueryEscape("text/markdown") }, world(ctx).Blob.signKey)
	default:
		return fmt.Errorf("unknown alteration %q", alteration)
	}
	if err != nil {
		return err
	}
	return clientUpload(ctx, []byte(body), ctype, raw, nil)
}

func clientUpload(ctx context.Context, body []byte, ctype, raw string, _ map[string]string) error {
	w := world(ctx).Blob
	if w.handlerSrv != nil && strings.HasPrefix(raw, "http://blob.test") {
		raw = strings.Replace(raw, "http://blob.test", w.handlerSrv.URL, 1)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, raw, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	w.httpResp = fetchResp{status: resp.StatusCode, body: b}
	return nil
}

func commitUpload(ctx context.Context, tenant string) error {
	return commitStaging(ctx, tenant, primaryUpload(world(ctx).Blob).StagingKey)
}

func commitSecondUpload(ctx context.Context, tenant string) error {
	u := world(ctx).Blob.uploadState().uploads
	if len(u) < 2 {
		return fmt.Errorf("need two uploads")
	}
	return commitStaging(ctx, tenant, u[1].StagingKey)
}

func commitKey(ctx context.Context, tenant, key string) error {
	return commitStaging(ctx, tenant, key)
}

func commitUploadNoTenant(ctx context.Context) error {
	w := world(ctx).Blob
	key := primaryUpload(w).StagingKey
	_, err := w.blob.Commit(ctx, key)
	w.lastErr = err
	w.uploadState().commits = append(w.uploadState().commits, commitResult{err: err})
	return nil
}

func commitStaging(ctx context.Context, tenant, stagingKey string) error {
	w := world(ctx).Blob
	cctx := w.tenantCtx(ctx, tenant)
	key, err := w.blob.Commit(cctx, stagingKey)
	w.lastErr = err
	w.uploadState().commits = append(w.uploadState().commits, commitResult{key: key, err: err})
	return nil
}

func assertCommitKey(ctx context.Context, name string) error {
	want := world(ctx).Blob.keyOf(name)
	cr := lastCommit(world(ctx).Blob)
	if cr.err != nil || cr.key != want {
		return fmt.Errorf("commit key %q err=%w, want %q", cr.key, cr.err, want)
	}
	return nil
}

func assertCommitKeyNamed(ctx context.Context, name string) error {
	b := world(ctx).Blob.content[name]
	want := blob.ContentKeyOf(b)
	cr := lastCommit(world(ctx).Blob)
	if cr.err != nil || cr.key != want {
		return fmt.Errorf("commit key %q err=%w", cr.key, cr.err)
	}
	return nil
}

func lastCommit(w *blobWorld) commitResult {
	cs := w.uploadState().commits
	if len(cs) == 0 {
		return commitResult{err: fmt.Errorf("no commit")}
	}
	return cs[len(cs)-1]
}

func assertFirstCommitCode(ctx context.Context, code string) error {
	cs := world(ctx).Blob.uploadState().commits
	if len(cs) < 1 {
		return fmt.Errorf("no commit")
	}
	return assertErrCode(cs[0].err, code)
}

func assertSecondCommitCode(ctx context.Context, code string) error {
	cs := world(ctx).Blob.uploadState().commits
	if len(cs) < 2 {
		return fmt.Errorf("need two commits")
	}
	return assertErrCode(cs[1].err, code)
}

func assertSecondCommitKey(ctx context.Context, name string) error {
	cs := world(ctx).Blob.uploadState().commits
	if len(cs) < 2 {
		return fmt.Errorf("need two commits")
	}
	want := world(ctx).Blob.keyOf(name)
	if cs[1].err != nil || cs[1].key != want {
		return fmt.Errorf("second commit %q err=%w", cs[1].key, cs[1].err)
	}
	return nil
}

func stagingKeysDiffer(ctx context.Context) error {
	u := world(ctx).Blob.uploadState().uploads
	if len(u) < 2 || u[0].StagingKey == u[1].StagingKey {
		return fmt.Errorf("staging keys should differ")
	}
	return nil
}

func uploadURLsDiffer(ctx context.Context) error {
	u := world(ctx).Blob.uploadState().uploads
	if len(u) < 2 || u[0].URL == u[1].URL {
		return fmt.Errorf("upload URLs should differ")
	}
	return nil
}

func stagingDiffersFromContent(ctx context.Context, name string) error {
	sk := primaryUpload(world(ctx).Blob).StagingKey
	ck := world(ctx).Blob.keyOf(name)
	if sk == ck {
		return fmt.Errorf("staging key equals content key")
	}
	return nil
}

func uploadURLNamesNoContentKey(ctx context.Context) error {
	u := primaryUpload(world(ctx).Blob).URL
	if strings.Contains(u, "sha256:") {
		return fmt.Errorf("upload URL names a content key")
	}
	return nil
}

func writeStagedString(ctx context.Context, body, ctype string) error {
	return writeStaged(ctx, []byte(body), ctype)
}

func writeStagedContent(ctx context.Context, name string) error {
	return writeStaged(ctx, world(ctx).Blob.content[name], "application/octet-stream")
}

func writeStaged(ctx context.Context, body []byte, ctype string) error {
	w := world(ctx).Blob
	u := primaryUpload(w)
	tenant := w.uploadState().tenant
	if tenant == "" {
		tenant = "acme"
	}
	return blob.WriteStagedObjectForTest(w.tenantCtx(ctx, tenant), w.store, core.TenantID(tenant), u.StagingKey, body, ctype)
}

func assertStagedNamed(ctx context.Context, tenant, name, ctype string) error {
	want := []byte(name)
	if b, ok := world(ctx).Blob.content[name]; ok {
		want = b
	}
	return assertStagedBody(ctx, tenant, want, ctype)
}

func assertStagedString(ctx context.Context, tenant, body string) error {
	return assertStagedBody(ctx, tenant, []byte(body), "")
}

func assertStagedBody(ctx context.Context, tenant string, want []byte, wantCT string) error {
	w := world(ctx).Blob
	sk := primaryUpload(w).StagingKey
	got, ct, err := w.store.ReadStagedObject(w.tenantCtx(ctx, tenant), core.TenantID(tenant), sk)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("staged content mismatch")
	}
	if wantCT != "" && ct != wantCT {
		return fmt.Errorf("content type %q, want %q", ct, wantCT)
	}
	return nil
}

func assertNoStaged(ctx context.Context, tenant string) error {
	w := world(ctx).Blob
	ok, err := w.store.StagedObjectExists(w.tenantCtx(ctx, tenant), core.TenantID(tenant), primaryUpload(w).StagingKey)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("staged object still exists")
	}
	return nil
}

func assertNoStagedSecond(ctx context.Context, tenant string) error {
	u := world(ctx).Blob.uploadState().uploads
	if len(u) < 2 {
		return fmt.Errorf("need two uploads")
	}
	w := world(ctx).Blob
	ok, err := w.store.StagedObjectExists(w.tenantCtx(ctx, tenant), core.TenantID(tenant), u[1].StagingKey)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("staged object still exists")
	}
	return nil
}

func sweepStaging(ctx context.Context) error {
	return world(ctx).Blob.store.SweepExpiredStagingSlots(ctx)
}

func sizedContentPlusOne(ctx context.Context, name string, mib, seed int) error {
	n := mib*1024*1024 + 1
	b := make([]byte, n)
	r := coretest.SeededRand(int64(seed))
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	world(ctx).Blob.content[name] = b
	return nil
}

func replaceUploadStagingKey(raw, stageKey string, _ []byte) (string, error) {
	return replaceUploadPathKeepSig(raw, 2, stageKey)
}

func replaceUploadTenant(raw, tenant string, _ []byte) (string, error) {
	return replaceUploadPathKeepSig(raw, 1, tenant)
}

func replaceUploadPathKeepSig(raw string, idx int, val string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	tenant, stage, err := uploadPathParts(u)
	if err != nil {
		return "", err
	}
	switch idx {
	case 1:
		tenant = val
	case 2:
		stage = val
	}
	prefix := u.Path
	if i := strings.Index(prefix, "/upload/"); i >= 0 {
		prefix = prefix[:i+len("/upload/")]
	} else {
		prefix = "/upload/"
	}
	u.Path = path.Clean(prefix + tenant + "/" + stage)
	return u.String(), nil
}

func alterUploadParam(raw, param string, f func(string) string, _ []byte) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(param, f(q.Get(param)))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func removeUploadSig(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Del("sig")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func resignUpload(raw string, signKey []byte) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	tenant, stage, err := uploadPathParts(u)
	if err != nil {
		return "", err
	}
	return rebuildUploadURL(u, tenant, stage, signKey)
}

func rebuildUploadURL(u *url.URL, tenant, stage string, signKey []byte) (string, error) {
	q := u.Query()
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	max, _ := strconv.ParseInt(q.Get("max"), 10, 64)
	ctEnc := q.Get("ct")
	ct, err := url.QueryUnescape(ctEnc)
	if err != nil {
		return "", err
	}
	sig := bddSignUpload(signKey, tenant, stage, exp, max, ct)
	q.Set("sig", sig)
	u.RawQuery = q.Encode()
	prefix := u.Path
	if i := strings.Index(prefix, "/upload/"); i >= 0 {
		prefix = prefix[:i+len("/upload/")]
	} else {
		prefix = "/upload/"
	}
	u.Path = path.Clean(prefix + tenant + "/" + stage)
	return u.String(), nil
}

func uploadPathParts(u *url.URL) (tenant, stage string, err error) {
	p := u.Path
	if i := strings.Index(p, "/upload/"); i >= 0 {
		p = p[i+len("/upload/"):]
	} else {
		return "", "", fmt.Errorf("bad upload path %q", u.Path)
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("bad upload path %q", u.Path)
	}
	return parts[0], parts[1], nil
}

func bddSignUpload(key []byte, tenant, stageKey string, exp, max int64, contentType string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("upload"))
	mac.Write([]byte{0})
	mac.Write([]byte(tenant))
	mac.Write([]byte{0})
	mac.Write([]byte(stageKey))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(max, 10)))
	mac.Write([]byte{0})
	mac.Write([]byte(contentType))
	return hex.EncodeToString(mac.Sum(nil))
}
