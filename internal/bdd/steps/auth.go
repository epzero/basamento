package steps

import (
	"context"
	"crypto/rsa"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/golang-jwt/jwt/v5"

	"github.com/epzero/basamento/auth/authn"
)

// oidcTestKeyPEM is a fixed 2048-bit RSA key: the component scenarios must not
// depend on key generation randomness or on a live issuer.
//
//go:embed testdata/oidc-rsa.pem
var oidcTestKeyPEM []byte

// oidcUnknownKeyPEM is a second RSA key that is not in the verifier key set.
//
//go:embed testdata/oidc-unknown-rsa.pem
var oidcUnknownKeyPEM []byte

type authWorld struct {
	mode     authn.Mode
	issuer   string
	audience string
	key      *rsa.PrivateKey
	verifier *authn.OIDC
}

func newAuthWorld() *authWorld { return &authWorld{} }

func (w *authWorld) sign(now time.Time, audience string, expires time.Time) (string, error) {
	if w.key == nil {
		return "", fmt.Errorf("no issuer key: the auth mode is not oidc")
	}
	claims := jwt.MapClaims{
		"iss":    w.issuer,
		"aud":    audience,
		"sub":    "ana",
		"iat":    jwt.NewNumericDate(now),
		"exp":    jwt.NewNumericDate(expires),
		"tenant": "acme",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "issuer-1"
	return token.SignedString(w.key)
}

func (w *authWorld) authenticate(ctx context.Context, raw string) error {
	if w.verifier == nil {
		return fmt.Errorf("no oidc verifier: the auth mode is not oidc")
	}
	_, err := w.verifier.Authenticate(ctx, raw)
	world(ctx).Core.lastErr = err
	return nil
}

func registerAuth(sc *godog.ScenarioContext) {
	sc.Step(`^auth mode "([^"]*)"$`, func(ctx context.Context, mode string) error {
		m, err := authn.ParseMode(mode)
		if err != nil {
			return err
		}
		world(ctx).Auth.mode = m
		return nil
	})
	sc.Step(`^auth mode "([^"]*)" with issuer "([^"]*)" and audience "([^"]*)"$`, func(ctx context.Context, mode, issuer, audience string) error {
		m, err := authn.ParseMode(mode)
		if err != nil {
			return err
		}
		key, err := jwt.ParseRSAPrivateKeyFromPEM(oidcTestKeyPEM)
		if err != nil {
			return fmt.Errorf("test issuer key: %w", err)
		}
		w := world(ctx).Auth
		w.mode = m
		w.issuer = issuer
		w.audience = audience
		w.key = key
		w.verifier, err = authn.NewOIDC(authn.OIDCOptions{
			Issuer:   issuer,
			Audience: audience,
			Keys:     authn.StaticKeys{"issuer-1": &key.PublicKey},
			Clock:    world(ctx).Core.clock,
		})
		return err
	})
	sc.Step(`^a request carries a token that is signed by an unknown key$`, func(ctx context.Context) error {
		w := world(ctx).Auth
		unknown, err := jwt.ParseRSAPrivateKeyFromPEM(oidcUnknownKeyPEM)
		if err != nil {
			return fmt.Errorf("test unknown signing key: %w", err)
		}
		now := world(ctx).Core.clock.Now()
		claims := jwt.MapClaims{
			"iss":    w.issuer,
			"aud":    w.audience,
			"sub":    "ana",
			"iat":    jwt.NewNumericDate(now),
			"exp":    jwt.NewNumericDate(now.Add(time.Hour)),
			"tenant": "acme",
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "issuer-1"
		raw, err := token.SignedString(unknown)
		if err != nil {
			return err
		}
		return w.authenticate(ctx, raw)
	})
	sc.Step(`^a request carries a token that is issued for audience "([^"]*)"$`, func(ctx context.Context, audience string) error {
		w := world(ctx).Auth
		raw, err := w.sign(world(ctx).Core.clock.Now(), audience, world(ctx).Core.clock.Now().Add(time.Hour))
		if err != nil {
			return err
		}
		return w.authenticate(ctx, raw)
	})
	sc.Step(`^a request carries a token that is expired one minute ago$`, func(ctx context.Context) error {
		w := world(ctx).Auth
		raw, err := w.sign(world(ctx).Core.clock.Now(), w.audience, world(ctx).Core.clock.Now().Add(-time.Minute))
		if err != nil {
			return err
		}
		return w.authenticate(ctx, raw)
	})

	sc.Step(`^"kit serve" is asked to listen on "([^"]*)"$`, func(ctx context.Context, addr string) error {
		w := world(ctx)
		extra := []string{"--listen.addr", addr}
		if w.Auth.mode != "" {
			extra = append(extra, "--auth.mode", string(w.Auth.mode))
		}
		return w.Config.startServe(extra...)
	})
	sc.Step(`^the server does not start and explains that auth is required for non-loopback addresses$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if w.kit == nil {
			return fmt.Errorf("kit serve was not started")
		}
		if code := w.kit.wait(); code == 0 {
			return fmt.Errorf("kit serve exited 0, want a refusal")
		}
		if strings.Contains(w.kit.stdout(), "ready ") {
			return fmt.Errorf("kit serve reported ready:\n%s", w.kit.stdout())
		}
		if !strings.Contains(w.kit.stderr(), "auth is required for non-loopback addresses") {
			return fmt.Errorf("stderr does not explain the auth requirement:\n%s", w.kit.stderr())
		}
		return nil
	})
}
