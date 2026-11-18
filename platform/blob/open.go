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

// Open selects an implementation from the location URL (REQ-BLOB-018).
//
// For file:// locations, temporary write files are created next to the final
// object path (fileblob no_tmp_dir) so the data directory may live on a mount
// other than the system temp directory. A process killed mid-write can leave
// stranded "<object>.<hex>.tmp" files with partial data; an operator may
// remove them when no write is in flight.
const minSignKeyLen = 32

func Open(ctx context.Context, raw string, opts Options) (*Store, http.Handler, error) {
	if len(opts.SignKey) > 0 && len(opts.SignKey) < minSignKeyLen {
		return nil, nil, core.NewError(core.CodeInvalidArgument, "blob SignKey must be at least 32 bytes", nil)
	}
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
	}
	var handler http.Handler
	if embedded {
		handler = (&URLHandler{Store: st}).Mount()
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
		dir := u.Path
		if dir == "" {
			dir = u.Host
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, mapError(err)
		}
		u.Path = filepath.Clean(dir)
		ensureFileBlobNoTmpDir(u)
	}
	b, err := blob.OpenBucket(ctx, u.String())
	if err != nil {
		return nil, mapOpenError(err)
	}
	return b, nil
}
