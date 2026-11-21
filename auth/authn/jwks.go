package authn

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
)

// ErrUnknownKey reports that a key set has no key for the requested id.
var ErrUnknownKey = errors.New("authn: unknown verification key")

var errNoUsableSigningKey = errors.New("authn: JWKS has no usable signing key")

// defaultMinRefresh is the minimum time between JWKS fetch attempts after the
// first one, unless JWKSOptions.MinRefresh overrides it.
const defaultMinRefresh = 30 * time.Second

const jwksFetchTimeout = 10 * time.Second

// maxJWKSBytes bounds an issuer's key set response; a key set is small.
const maxJWKSBytes = 1 << 20

// JWKSOptions configures a JWKS key set read from an issuer URL.
type JWKSOptions struct {
	// URL is the issuer JWKS endpoint. It must be https, or http when the host
	// is loopback. Credentials in the URL are never echoed in errors.
	URL string
	// Client performs fetches. When nil, the default client applies a 10 s
	// timeout and does not follow redirects; a supplied client keeps its own
	// timeout and redirect policy.
	Client *http.Client
	// Clock records fetch-attempt times for rate limiting.
	Clock core.Clock
	// MinRefresh is the minimum time between fetch attempts after the first.
	// Zero means defaultMinRefresh; negative values are rejected.
	MinRefresh time.Duration
}

// JWKS validates tokens against keys fetched from an issuer's JSON Web Key Set
// URL. The set is cached; a token naming an unknown key id may trigger one
// refresh subject to single-flight and rate limits.
type JWKS struct {
	url        string
	client     *http.Client
	clock      core.Clock
	minRefresh time.Duration

	mu          sync.RWMutex
	keys        map[string]any
	lastAttempt time.Time

	flightMu sync.Mutex
	flight   *jwksFlight
}

type jwksFlight struct {
	done chan struct{}
	err  error
}

// NewJWKS returns a key set for opts.
func NewJWKS(opts JWKSOptions) (*JWKS, error) {
	if opts.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.jwks clock is required", map[string]any{"key": "auth.oidc.jwks"})
	}
	minRefresh := opts.MinRefresh
	if minRefresh == 0 {
		minRefresh = defaultMinRefresh
	} else if minRefresh < 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.jwks min refresh must not be negative", map[string]any{"key": "auth.oidc.jwks"})
	}
	u, err := url.Parse(opts.URL)
	if err != nil || u.Host == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.jwks must be an http or https URL", map[string]any{"key": "auth.oidc.jwks"})
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.jwks must use https except on loopback", map[string]any{"key": "auth.oidc.jwks"})
		}
	default:
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.jwks must be an http or https URL", map[string]any{"key": "auth.oidc.jwks"})
	}
	client := opts.Client
	if client == nil {
		client = defaultJWKSHTTPClient()
	}
	return &JWKS{
		url:        opts.URL,
		client:     client,
		clock:      opts.Clock,
		minRefresh: minRefresh,
	}, nil
}

func defaultJWKSHTTPClient() *http.Client {
	return &http.Client{
		Timeout: jwksFetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// VerificationKey returns the key named kid. It serves a cached key under a
// short read lock, refreshes on a miss with single-flight and rate limits, and
// reports ErrUnknownKey when the set still lacks the key after a refresh.
func (j *JWKS) VerificationKey(ctx context.Context, kid string) (any, error) {
	j.mu.RLock()
	key, ok := j.matchLocked(kid)
	j.mu.RUnlock()
	if ok {
		return key, nil
	}
	return j.lookupAfterRefresh(ctx, kid)
}

func (j *JWKS) lookupAfterRefresh(ctx context.Context, kid string) (any, error) {
	waiter, cooldown := j.beginRefresh(ctx)
	if cooldown {
		return nil, ErrUnknownKey
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-waiter.done:
		if waiter.err != nil {
			return nil, waiter.err
		}
		j.mu.RLock()
		key, ok := j.matchLocked(kid)
		j.mu.RUnlock()
		if ok {
			return key, nil
		}
		return nil, ErrUnknownKey
	}
}

func (j *JWKS) beginRefresh(ctx context.Context) (*jwksFlight, bool) {
	j.flightMu.Lock()
	if j.flight != nil {
		w := j.flight
		j.flightMu.Unlock()
		return w, false
	}

	j.mu.Lock()
	now := j.clock.Now()
	if !j.lastAttempt.IsZero() && now.Sub(j.lastAttempt) < j.minRefresh {
		j.mu.Unlock()
		j.flightMu.Unlock()
		return nil, true
	}
	j.lastAttempt = now
	j.mu.Unlock()

	w := &jwksFlight{done: make(chan struct{})}
	j.flight = w
	j.flightMu.Unlock()

	go func() {
		defer close(w.done)
		defer func() {
			j.flightMu.Lock()
			j.flight = nil
			j.flightMu.Unlock()
		}()
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jwksFetchTimeout)
		defer cancel()
		w.err = j.fetch(fetchCtx)
	}()

	return w, false
}

func (j *JWKS) matchLocked(kid string) (any, bool) {
	if kid != "" {
		key, ok := j.keys[kid]
		return key, ok
	}
	if len(j.keys) == 1 {
		for _, key := range j.keys {
			return key, true
		}
	}
	return nil, false
}

func (j *JWKS) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authn: JWKS endpoint answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes))
	if err != nil {
		return err
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return err
	}
	keys := make(map[string]any, len(set.Keys))
	for _, key := range set.Keys {
		if key.Use == "enc" {
			continue
		}
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		public, err := key.publicKey()
		if err != nil {
			continue
		}
		keys[key.Kid] = public
	}
	if len(keys) == 0 {
		return errNoUsableSigningKey
	}
	j.mu.Lock()
	j.keys = keys
	j.mu.Unlock()
	return nil
}

// jwk is the subset of RFC 7517 fields an OIDC verifier needs.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) publicKey() (any, error) {
	switch k.Kty {
	case "RSA":
		return k.rsaKey()
	case "EC":
		return k.ecKey()
	case "OKP":
		return k.ed25519Key()
	default:
		return nil, fmt.Errorf("authn: unsupported key type %q", k.Kty)
	}
}

func (k jwk) rsaKey() (any, error) {
	n, err := decodeJWKNumber(k.N)
	if err != nil {
		return nil, err
	}
	e, err := decodeJWKNumber(k.E)
	if err != nil {
		return nil, err
	}
	if n.Sign() <= 0 || e.Sign() <= 0 || !e.IsInt64() {
		return nil, errors.New("authn: malformed RSA key")
	}
	exponent := e.Int64()
	if exponent > 1<<31-1 {
		return nil, errors.New("authn: RSA exponent is too large")
	}
	return &rsa.PublicKey{N: n, E: int(exponent)}, nil
}

func (k jwk) ecKey() (any, error) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("authn: unsupported curve %q", k.Crv)
	}
	x, err := decodeJWKBytes(k.X)
	if err != nil {
		return nil, err
	}
	y, err := decodeJWKBytes(k.Y)
	if err != nil {
		return nil, err
	}
	size := (curve.Params().BitSize + 7) / 8
	if len(x) > size || len(y) > size {
		return nil, errors.New("authn: EC key coordinate is too long")
	}
	point := make([]byte, 1+2*size)
	point[0] = 4 // uncompressed point, SEC 1 §2.3.3
	copy(point[1+size-len(x):1+size], x)
	copy(point[1+2*size-len(y):], y)
	return ecdsa.ParseUncompressedPublicKey(curve, point)
}

func (k jwk) ed25519Key() (any, error) {
	if k.Crv != "Ed25519" {
		return nil, fmt.Errorf("authn: unsupported curve %q", k.Crv)
	}
	raw, err := decodeJWKBytes(k.X)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("authn: malformed Ed25519 key")
	}
	return ed25519.PublicKey(raw), nil
}

func decodeJWKNumber(value string) (*big.Int, error) {
	raw, err := decodeJWKBytes(value)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("authn: empty JWK number")
	}
	return new(big.Int).SetBytes(raw), nil
}

func decodeJWKBytes(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("authn: missing JWK field")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(value)
}
