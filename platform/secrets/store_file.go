package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileStore persists wrapped keysets as one JSON file per tenant.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore uses dir, creating it when missing.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir}, nil
}

func (s *FileStore) path(tenant string) string {
	return filepath.Join(s.dir, tenant+".json")
}

func (s *FileStore) Get(tenant string) (StoredKeyset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(tenant))
	if errors.Is(err, os.ErrNotExist) {
		return StoredKeyset{}, false, nil
	}
	if err != nil {
		return StoredKeyset{}, false, err
	}
	var rec StoredKeyset
	if err := json.Unmarshal(b, &rec); err != nil {
		return StoredKeyset{}, false, err
	}
	return rec, true, nil
}

func (s *FileStore) CreateIfAbsent(tenant string, record StoredKeyset) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path(tenant), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	return true, writeRecord(f, record)
}

func (s *FileStore) CompareAndSwap(tenant string, expectRevision uint64, record StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := os.ReadFile(s.path(tenant))
	if errors.Is(err, os.ErrNotExist) {
		return errInvalidArgument("keyset revision conflict")
	}
	if err != nil {
		return err
	}
	var old StoredKeyset
	if err := json.Unmarshal(cur, &old); err != nil {
		return err
	}
	if old.Revision != expectRevision {
		return errInvalidArgument("keyset revision conflict")
	}
	f, err := os.OpenFile(s.path(tenant), os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return writeRecord(f, record)
}

func (s *FileStore) Replace(tenant string, record StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path(tenant), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return writeRecord(f, record)
}

func (s *FileStore) Count() (int, error) {
	ents, err := s.Tenants()
	return len(ents), err
}

func (s *FileStore) Tenants() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		out = append(out, e.Name()[:len(e.Name())-len(".json")])
	}
	return out, nil
}

func writeRecord(w *os.File, record StoredKeyset) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// MemStore is an in-memory KeysetStore for unit tests.
type MemStore struct {
	mu   sync.Mutex
	data map[string]StoredKeyset
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{data: map[string]StoredKeyset{}}
}

func (s *MemStore) Get(tenant string) (StoredKeyset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[tenant]
	return r, ok, nil
}

func (s *MemStore) CreateIfAbsent(tenant string, record StoredKeyset) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.data[tenant]; ok {
		_ = existing
		return false, nil
	}
	s.data[tenant] = record
	return true, nil
}

func (s *MemStore) CompareAndSwap(tenant string, expectRevision uint64, record StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data[tenant]
	if !ok || old.Revision != expectRevision {
		return fmt.Errorf("revision conflict")
	}
	s.data[tenant] = record
	return nil
}

func (s *MemStore) Replace(tenant string, record StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[tenant] = record
	return nil
}

func (s *MemStore) Count() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data), nil
}

func (s *MemStore) Tenants() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for k := range s.data {
		out = append(out, k)
	}
	return out, nil
}
