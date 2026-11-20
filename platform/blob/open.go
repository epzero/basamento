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

	"github.com/epzero/basamento/core"
)

const minSignKeyLen = 32

// Open selects an implementation from the location URL (REQ-BLOB-018).
//
// For file:// locations, temporary write files are created next to the final
// object path (fileblob no_tmp_dir) so the data directory may live on a mount
// other than the system temp directory. A process killed mid-write can leave
// stranded "<object>.<hex>.tmp" files with partial data; an operator may
// remove them when no write is in flight.
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
	embedded := u.Scheme == "file"
	if embedded && len(opts.SignKey) > 0 && len(opts.SignKey) < minSignKeyLen {
		return nil, nil, core.NewError(core.CodeInvalidArgument, "blob SignKey must be at least 32 bytes", nil)
	}
	bucket, err := openBucket(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var signKey []byte
	if embedded {
		signKey = append([]byte(nil), opts.SignKey...)
	}
	st := &Store{
		bucket:   wrapBucket(bucket),
		clock:    opts.clock(),
		maxTTL:   opts.maxTTL(),
		embedded: embedded,
		baseURL:  strings.TrimRight(opts.BaseURL, "/"),
		signKey:  signKey,
		rand:     opts.rand(),
	}
	if st.embedded {
		st.sweepAtOpen(ctx)
	}
	var handler http.Handler
	if embedded {
		handler = newEmbeddedHandler(st)
	}
	return st, handler, nil
}

func ensureFileBlobNoTmpDir(u *url.URL) {
	q := u.Query()
	if q.Get("no_tmp_dir") != "" {
		return
	}
	q.Set("no_tmp_dir", "true")
	u.RawQuery = q.Encode()
}

func openBucket(ctx context.Context, u *url.URL) (*blob.Bucket, error) {
	if u.Scheme == "file" {
		dir, err := fileDir(u)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, mapError(err)
		}
		u.Host = ""
		u.Path = dir
		u.RawPath = ""
		ensureFileBlobNoTmpDir(u)
	}
	b, err := blob.OpenBucket(ctx, u.String())
	if err != nil {
		return nil, mapOpenError(err)
	}
	return b, nil
}

// fileDir returns the absolute filesystem directory of a file URL. Go parses
// file://data/blobs with Host "data" and Path "/blobs"; fileblob uses only the
// path, which would open /blobs. A host other than localhost is therefore the
// first path segment and is joined with the path (CR-03).
func fileDir(u *url.URL) (string, error) {
	dir := u.Path
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		dir = filepath.Join(u.Host, dir)
	}
	if dir == "" {
		return "", core.NewError(core.CodeInvalidArgument, "file URL has no path", nil)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", mapError(err)
	}
	return abs, nil
}
