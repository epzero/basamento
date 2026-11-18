package blob

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"

	"github.com/epzero/basamento/platform"
)

// Open selects an implementation from the location URL (REQ-BLOB-018).
func Open(ctx context.Context, raw string, opts Options) (*Store, http.Handler, error) {
	if raw == "" {
		return nil, nil, unsupportedLocation(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil, nil, unsupportedLocation(raw)
	}
	switch u.Scheme {
	case "file", "s3", "gs", "azblob":
	default:
		return nil, nil, unsupportedLocation(raw)
	}
	bucket, err := openBucket(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	embedded := u.Scheme == "file"
	st := &Store{
		bucket:   bucket,
		clock:    opts.clock(),
		maxTTL:   opts.maxTTL(),
		embedded: embedded,
		baseURL:  strings.TrimRight(opts.BaseURL, "/"),
		signKey:  append([]byte(nil), opts.SignKey...),
		location: raw,
		openOpts: opts,
	}
	var handler http.Handler
	if embedded {
		if st.baseURL == "" {
			st.baseURL = "http://blob.test" + HandlerRoute()
		}
		handler = (&URLHandler{Store: st}).Mount()
	}
	return st, handler, nil
}

func openBucket(ctx context.Context, u *url.URL) (*blob.Bucket, error) {
	if u.Scheme == "file" {
		dir := u.Path
		if dir == "" {
			dir = u.Host
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, mapError(err)
		}
		u.Path = filepath.Clean(dir)
	}
	b, err := blob.OpenBucket(ctx, u.String())
	if err != nil {
		return nil, mapError(err)
	}
	return b, nil
}

// OpenResult wraps Open for callers that need the port type.
func OpenBlob(ctx context.Context, raw string, opts Options) (OpenResult, error) {
	st, h, err := Open(ctx, raw, opts)
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Blob: st, Handler: h, Store: st}, nil
}

var (
	_ platform.Blob = (*Store)(nil)
)
