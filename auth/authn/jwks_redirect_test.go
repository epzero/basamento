package authn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core/coretest"
)

func TestJWKSSuppliedClientRejectsRedirects(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		serveJWKS(w, []map[string]any{rsaJWK("k1", &key.PublicKey)})
	}))
	t.Cleanup(target.Close)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(source.Close)

	supplied := &http.Client{}
	set, err := NewJWKS(JWKSOptions{URL: source.URL, Clock: clock, Client: supplied})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.VerificationKey(context.Background(), "k1"); err == nil {
		t.Fatal("want redirect failure")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target saw %d requests, want 0", targetHits.Load())
	}
	if supplied.CheckRedirect != nil {
		t.Fatal("caller client CheckRedirect was mutated")
	}
}

func TestJWKSRedirectsNeverFollowedWithNilClient(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(source.Close)
	set := testJWKS(t, source.URL, clock, 0)
	if _, err := set.VerificationKey(context.Background(), "k1"); err == nil {
		t.Fatal("want redirect failure")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target saw %d requests, want 0", targetHits.Load())
	}
}

func serveJWKS(w http.ResponseWriter, keys []map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(map[string]any{"keys": keys})
	_, _ = w.Write(body)
}
