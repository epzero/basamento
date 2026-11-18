package blob

import (
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
	// BaseURL is the public origin for proxied read URLs (embedded file only).
	BaseURL string
	// SignKey is the HMAC key for proxied read URLs (embedded file only).
	SignKey []byte
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
