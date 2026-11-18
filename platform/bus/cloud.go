package bus

import (
	"context"
	"net/url"
	"time"

	"github.com/nats-io/nats.go"
)

// OpenCloud connects to a NATS cluster at rawURL (scheme nats:// or nats+srv://).
// Credential-bearing URLs are never logged.
func OpenCloud(ctx context.Context, rawURL string, opts Options) (*Store, error) {
	opts = opts.withDefaults()
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, invalidSubject()
	}
	if u.Scheme != "nats" && u.Scheme != "tls" && u.Scheme != "nats+tls" {
		return nil, unavailable()
	}
	pass, _ := u.User.Password()
	var nopts []nats.Option
	nopts = append(nopts, nats.Timeout(2*time.Second))
	if u.User != nil {
		nopts = append(nopts, nats.UserInfo(u.User.Username(), pass))
	}
	nc, err := nats.Connect(rawURL, nopts...)
	if err != nil {
		return nil, mapConnErr(err)
	}
	st, err := connectAndOpen(nc, opts, nil)
	if err != nil {
		return nil, err
	}
	_ = ctx
	return st, nil
}
