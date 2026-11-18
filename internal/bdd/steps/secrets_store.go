package steps

import (
	"fmt"
	"sync"

	"github.com/epzero/basamento/platform/secrets"
)

type bddKeysetStore struct {
	mu   sync.Mutex
	data map[string]secrets.StoredKeyset
}

func newBDDKeysetStore() *bddKeysetStore {
	return &bddKeysetStore{data: map[string]secrets.StoredKeyset{}}
}

func (s *bddKeysetStore) Get(tenant string) (secrets.StoredKeyset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[tenant]
	return r, ok, nil
}

func (s *bddKeysetStore) CreateIfAbsent(tenant string, record secrets.StoredKeyset) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[tenant]; ok {
		return false, nil
	}
	s.data[tenant] = record
	return true, nil
}

func (s *bddKeysetStore) CompareAndSwap(tenant string, expectRevision uint64, record secrets.StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data[tenant]
	if !ok || old.Revision != expectRevision {
		return fmt.Errorf("revision conflict")
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

func (s *bddKeysetStore) Count() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data), nil
}

func (s *bddKeysetStore) Tenants() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for k := range s.data {
		out = append(out, k)
	}
	return out, nil
}
