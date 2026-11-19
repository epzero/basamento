package steps

import (
	"context"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

type bddKeysetStore struct {
	mu   sync.Mutex
	data map[string]secrets.StoredKeyset
}

func newBDDKeysetStore() *bddKeysetStore {
	return &bddKeysetStore{data: map[string]secrets.StoredKeyset{}}
}

func (s *bddKeysetStore) Get(ctx context.Context, tenant string) (secrets.StoredKeyset, bool, error) {
	if err := ctx.Err(); err != nil {
		return secrets.StoredKeyset{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[tenant]
	return r, ok, nil
}

func (s *bddKeysetStore) CreateIfAbsent(ctx context.Context, tenant string, record secrets.StoredKeyset) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[tenant]; ok {
		return false, nil
	}
	s.data[tenant] = record
	return true, nil
}

func (s *bddKeysetStore) CompareAndSwap(ctx context.Context, tenant string, expectRevision uint64, record secrets.StoredKeyset) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data[tenant]
	if !ok || old.Revision != expectRevision {
		return &core.Error{Code: core.CodeConflict, Message: "keyset was changed by another writer", Retryable: true}
	}
	s.data[tenant] = record
	return nil
}

func (s *bddKeysetStore) Replace(tenant string, record secrets.StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[tenant] = record
	return nil
}

func (s *bddKeysetStore) Count(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data), nil
}

func (s *bddKeysetStore) Tenants(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for k := range s.data {
		out = append(out, k)
	}
	return out, nil
}
