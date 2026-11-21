package authn

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
)

func TestJWKSRefreshesOnUnknownKey(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	var mu sync.Mutex
	kid := "k1"
	server, requests := jwksServer(t, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return []map[string]any{rsaJWK(kid, &key.PublicKey)}
	})
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}

	mu.Lock()
	kid = "k2"
	mu.Unlock()
	clock.Advance(defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k2"); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if *requests != 2 {
		t.Fatalf("requests = %d, want 2", *requests)
	}

	if _, err := set.VerificationKey(context.Background(), "missing"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}
func TestJWKSBlockedFetchDoesNotStallCacheHit(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	release := make(chan struct{})
	blocked := make(chan struct{})
	var requests atomic.Int32
	var blockedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
			return
		}
		blockedOnce.Do(func() { close(blocked) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k2", &key.PublicKey)}})
	}))
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		closeRelease()
		server.Close()
	})
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(defaultMinRefresh)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := set.VerificationKey(context.Background(), "k2"); err != nil {
			t.Error(err)
		}
	}()
	<-blocked
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatalf("cached k1 during fetch: %v", err)
	}
	closeRelease()
	<-done
}

func TestJWKSSingleFlight(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var requests atomic.Int32
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		once.Do(func() { close(started) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
	}))
	t.Cleanup(server.Close)
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	const n = 8
	errs := make(chan error, n)
	keys := make(chan any, n)
	for i := 0; i < n; i++ {
		go func() {
			k, err := set.VerificationKey(context.Background(), "k1")
			keys <- k
			errs <- err
		}()
	}
	<-started
	close(release)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		got := <-keys
		pub, ok := got.(*rsa.PublicKey)
		if !ok || !pub.Equal(&key.PublicKey) {
			t.Fatalf("key %d: %#v", i, got)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestJWKSCooldownAfterClockStepBack(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	var mu sync.Mutex
	kid := "k1"
	server, requests := jwksServer(t, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return []map[string]any{rsaJWK(kid, &key.PublicKey)}
	})
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}
	t0 := clock.Now()
	clock.Set(t0.Add(-time.Hour))
	mu.Lock()
	kid = "k2"
	mu.Unlock()
	if _, err := set.VerificationKey(context.Background(), "k2"); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("after clock step back: requests = %d, want 2", *requests)
	}
	if _, err := set.VerificationKey(context.Background(), "missing"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("immediate second miss: %v", err)
	}
	if *requests != 2 {
		t.Fatalf("within cooldown: requests = %d, want 2", *requests)
	}
	clock.Advance(defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "missing"); !errors.Is(err, ErrUnknownKey) {
		t.Fatal(err)
	}
	if *requests != 3 {
		t.Fatalf("after advance: requests = %d, want 3", *requests)
	}
}

func TestJWKSRememberFetchErrorOnEmptyCache(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	var mu sync.Mutex
	status := http.StatusInternalServerError
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		st := status
		mu.Unlock()
		if st != http.StatusOK {
			w.WriteHeader(st)
			_, _ = w.Write([]byte("no"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
	}))
	t.Cleanup(server.Close)
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	_, err := set.VerificationKey(context.Background(), "k1")
	if err == nil || errors.Is(err, ErrUnknownKey) {
		t.Fatalf("first lookup: %v", err)
	}
	want := err.Error()
	_, err = set.VerificationKey(context.Background(), "k1")
	if err == nil || errors.Is(err, ErrUnknownKey) || err.Error() != want {
		t.Fatalf("second lookup in cooldown: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	mu.Lock()
	status = http.StatusOK
	mu.Unlock()
	clock.Advance(defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	set.mu.Lock()
	set.keys = nil
	set.mu.Unlock()
	if _, err := set.lookupAfterRefresh(context.Background(), "other"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("after success with empty cache on cooldown: %v", err)
	}
}

func TestJWKSFetchFailureKeepsUnknownKeyWithPopulatedCache(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	ok := true
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
			return
		}
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	ok = false
	clock.Advance(defaultMinRefresh)
	_, err := set.VerificationKey(context.Background(), "missing")
	if err == nil || errors.Is(err, ErrUnknownKey) {
		t.Fatalf("failed refresh: %v", err)
	}
	if requests != 2 {
		t.Fatalf("failed refresh: requests = %d, want 2", requests)
	}
	_, err = set.VerificationKey(context.Background(), "missing")
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("cooldown with populated cache: %v", err)
	}
	if requests != 2 {
		t.Fatalf("cooldown: requests = %d, want 2", requests)
	}
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
}

func TestJWKSLookupAfterRefreshRereadsCacheOnCooldown(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	server, requests := jwksServer(t, func() []map[string]any {
		return []map[string]any{rsaJWK("k1", &key.PublicKey)}
	})
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	got, err := set.lookupAfterRefresh(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if pub, ok := got.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		t.Fatalf("lookupAfterRefresh: %#v", got)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}
}

func TestJWKSCooldownRateLimits(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	server, requests := jwksServer(t, func() []map[string]any {
		return []map[string]any{rsaJWK("k1", &key.PublicKey)}
	})
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("first fetch not delayed: requests = %d, want 1", *requests)
	}
	if _, err := set.VerificationKey(context.Background(), "missing"); !errors.Is(err, ErrUnknownKey) {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("within cooldown: requests = %d, want 1", *requests)
	}
	clock.Advance(defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "missing"); !errors.Is(err, ErrUnknownKey) {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("after cooldown: requests = %d, want 2", *requests)
	}
}
func TestJWKSCancelledWaiterDoesNotStopFetch(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var requests atomic.Int32
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		once.Do(func() { close(started) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
	}))
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		closeRelease()
		server.Close()
	})
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)

	ctxA, cancelA := context.WithCancel(context.Background())
	errA := make(chan error, 1)
	go func() {
		_, err := set.VerificationKey(ctxA, "k1")
		errA <- err
	}()
	<-started

	errB := make(chan error, 1)
	keyB := make(chan any, 1)
	go func() {
		k, err := set.VerificationKey(context.Background(), "k1")
		errB <- err
		keyB <- k
	}()

	cancelA()
	if err := <-errA; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller A: %v, want context.Canceled", err)
	}
	closeRelease()
	if err := <-errB; err != nil {
		t.Fatalf("caller B: %v", err)
	}
	got := <-keyB
	pub, ok := got.(*rsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		t.Fatalf("caller B key: %#v", got)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestJWKSFetchTimeout(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
	}))
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		closeRelease()
		server.Close()
	})
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	set.client = &http.Client{}
	set.fetchTimeout = time.Nanosecond
	_, err := set.VerificationKey(context.Background(), "k1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
