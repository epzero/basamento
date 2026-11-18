package bus

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

func loopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// OpenCloud connects to a NATS cluster at rawURL (scheme nats or tls).
// Userinfo on scheme nats is accepted only for loopback hosts; otherwise use tls.
// Credential-bearing URLs are never logged. ctx bounds connection and asset setup.
func OpenCloud(ctx context.Context, rawURL string, opts Options) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, mapConnErr(err)
	}
	opts = opts.withDefaults()
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, invalidURL()
	}
	if u.Scheme != "nats" && u.Scheme != "tls" {
		return nil, invalidURL()
	}
	if u.Scheme == "nats" && u.User != nil && !loopbackHost(u.Hostname()) {
		return nil, plaintextCredential()
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
	st, err := connectAndOpen(ctx, nc, opts, nil)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return st, nil
}
