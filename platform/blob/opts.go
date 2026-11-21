package blob

import (
	"crypto/rand"
	"io"
	"time"

	"github.com/epzero/basamento/core"
)

// DefaultMaxURLTTL is the longest proxied or presigned read URL lifetime (REQ-BLOB-017).
const DefaultMaxURLTTL = 7 * 24 * time.Hour

// Options configure a store opened from a URL.
type Options struct {
	// Clock supplies time for URL expiry; defaults to core.SystemClock{}.
	Clock core.Clock
	// MaxURLTTL caps SignedURL lifetimes; defaults to DefaultMaxURLTTL.
	MaxURLTTL time.Duration
	// BaseURL is required on a file-backed store for SignedURL: the public origin
	// of proxied read URLs (scheme + host + HandlerRoute prefix). Wiring sets it.
	BaseURL string
	// SignKey is required on a file-backed store for SignedURL and the proxied
	// read handler. When non-empty it must be at least 32 bytes. An empty key
	// means proxied URLs are disabled. Wiring sets it.
	SignKey []byte
	// RandReader supplies random bytes for staging keys; defaults to
	// crypto/rand.Reader.
	RandReader io.Reader
}

func (o *Options) clock() core.Clock {
	if o.Clock != nil {
		return o.Clock
	}
	return core.SystemClock{}
}

func (o *Options) maxTTL() time.Duration {
	if o.MaxURLTTL > 0 {
		return o.MaxURLTTL
	}
	return DefaultMaxURLTTL
}

func (o *Options) rand() io.Reader {
	if o.RandReader != nil {
		return o.RandReader
	}
	return rand.Reader
}
