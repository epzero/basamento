package temporal

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

// manualClock advances only when a test calls Advance and wakes the timers
// whose deadline has been reached, so timer tests never sleep.
type manualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []manualWaiter
}

type manualWaiter struct {
	at time.Time
	ch chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_700_000_000, 0).UTC()}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, manualWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, waiter := range c.waiters {
		if waiter.at.After(c.now) {
			kept = append(kept, waiter)
			continue
		}
		waiter.ch <- c.now
	}
	c.waiters = kept
}

// testKeyService wraps keysets with AES-GCM under a fixed key. It is a test
// double for the key service the composition root supplies in production.
type testKeyService struct{ id string }

func (k testKeyService) MasterKeyID() string { return k.id }
func (k testKeyService) Close() error        { return nil }

func (k testKeyService) Wrap(_ context.Context, tenant string, plaintext []byte) ([]byte, error) {
	key := sha256.Sum256([]byte(k.id + "/" + tenant))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	return gcm.Seal(nonce, nonce, plaintext, []byte(tenant)), nil
}

func (k testKeyService) Unwrap(_ context.Context, tenant string, ciphertext []byte) ([]byte, error) {
	key := sha256.Sum256([]byte(k.id + "/" + tenant))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, core.NewError(core.CodeInvalidArgument, "test key service: short ciphertext", nil)
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], []byte(tenant))
}

type testKeysetStore struct {
	mu   sync.Mutex
	data map[string]secrets.StoredKeyset
}

func newTestKeysetStore() *testKeysetStore {
	return &testKeysetStore{data: map[string]secrets.StoredKeyset{}}
}

func (s *testKeysetStore) Get(_ context.Context, tenant string) (secrets.StoredKeyset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[tenant]
	return r, ok, nil
}

func (s *testKeysetStore) CreateIfAbsent(_ context.Context, tenant string, record secrets.StoredKeyset) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[tenant]; ok {
		return false, nil
	}
	s.data[tenant] = record
	return true, nil
}

func (s *testKeysetStore) CompareAndSwap(_ context.Context, tenant string, expect uint64, record secrets.StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data[tenant]
	if !ok || old.Revision != expect {
		return core.NewError(core.CodeConflict, "keyset revision conflict", nil)
	}
	s.data[tenant] = record
	return nil
}

func (s *testKeysetStore) Count(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data), nil
}

func (s *testKeysetStore) Tenants(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for tenant := range s.data {
		out = append(out, tenant)
	}
	return out, nil
}
