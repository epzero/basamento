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

// maxJWKSBytes bounds an issuer's key set response; a key set is small.
const maxJWKSBytes = 1 << 20

// JWKS validates tokens against keys fetched from an issuer's JSON Web Key Set
// URL. The set is cached; a token naming an unknown key id triggers one refresh,
// which is how an issuer rotates keys without a restart.
type JWKS struct {
	url    string
	client *http.Client

	mu   sync.Mutex
	keys map[string]any
}

// NewJWKS returns a key set read from rawURL. The default HTTP client has a
// finite timeout so a slow issuer cannot hold a request open.
func NewJWKS(rawURL string) (*JWKS, error) {
	return NewJWKSClient(rawURL, &http.Client{Timeout: 10 * time.Second})
}

// NewJWKSClient is NewJWKS with a caller-supplied client, such as one with a
// private transport. A nil client gets the default.
func NewJWKSClient(rawURL string, client *http.Client) (*JWKS, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.jwks must be an http or https URL", map[string]any{"key": "auth.oidc.jwks"})
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &JWKS{url: rawURL, client: client}, nil
}

// VerificationKey returns the key named kid. It serves a cached key without a
// request, refreshes on a miss and reports ErrUnknownKey when the refreshed set
// still lacks the key.
func (j *JWKS) VerificationKey(ctx context.Context, kid string) (any, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if key, ok := j.match(kid); ok {
		return key, nil
	}
	if err := j.refresh(ctx); err != nil {
		return nil, err
	}
	if key, ok := j.match(kid); ok {
		return key, nil
	}
	return nil, ErrUnknownKey
}

func (j *JWKS) match(kid string) (any, bool) {
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

func (j *JWKS) refresh(ctx context.Context) error {
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
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		public, err := key.publicKey()
		if err != nil {
			continue
		}
		keys[key.Kid] = public
	}
	j.keys = keys
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
