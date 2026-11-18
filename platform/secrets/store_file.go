package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxTenantIDLen = 512

// FileStore persists wrapped keysets as one JSON file per tenant.
//
// Guarantees: consistent reads and atomic replacement for a single process.
// A process-local mutex serializes writers. Other processes are not
// coordinated; do not share one directory across processes without an external
// lock. After a crash, a record is either the previous revision or the new one
// (never a torn JSON document).
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore uses dir, creating it when missing with mode 0700.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir}, nil
}

func validateTenantID(tenant string) error {
	if tenant == "" {
		return fmt.Errorf("tenant id is required")
	}
	if len(tenant) > maxTenantIDLen {
		return fmt.Errorf("tenant id is too long")
	}
	if strings.Contains(tenant, "\x00") {
		return fmt.Errorf("tenant id is invalid")
	}
	if strings.Contains(tenant, "..") || strings.ContainsAny(tenant, `/\`) {
		return fmt.Errorf("tenant id is invalid")
	}
	return nil
}

func (s *FileStore) path(tenant string) (string, error) {
	if err := validateTenantID(tenant); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(tenant))
	name := hex.EncodeToString(sum[:]) + ".json"
	return filepath.Join(s.dir, name), nil
}

func (s *FileStore) Get(tenant string) (StoredKeyset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(tenant)
	if err != nil {
		return StoredKeyset{}, false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return StoredKeyset{}, false, nil
	}
	if err != nil {
		return StoredKeyset{}, false, err
	}
	var onDisk fileKeysetRecord
	if err := json.Unmarshal(b, &onDisk); err != nil {
		return StoredKeyset{}, false, fmt.Errorf("corrupt keyset record: %w", err)
	}
	return onDisk.StoredKeyset, true, nil
}

func (s *FileStore) CreateIfAbsent(tenant string, record StoredKeyset) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(tenant)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := writeRecordFile(f, tenant, record); err != nil {
		_ = f.Close()
		_ = os.Remove(p)
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *FileStore) CompareAndSwap(tenant string, expectRevision uint64, record StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(tenant)
	if err != nil {
		return err
	}
	cur, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return errInvalidArgument("keyset revision conflict")
	}
	if err != nil {
		return err
	}
	var old fileKeysetRecord
	if err := json.Unmarshal(cur, &old); err != nil {
		return fmt.Errorf("corrupt keyset record: %w", err)
	}
	if old.Revision != expectRevision {
		return errInvalidArgument("keyset revision conflict")
	}
	return writeRecordAtomic(p, tenant, record)
}

func (s *FileStore) Replace(tenant string, record StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(tenant)
	if err != nil {
		return err
	}
	return writeRecordAtomic(p, tenant, record)
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
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var onDisk fileKeysetRecord
		if err := json.Unmarshal(b, &onDisk); err != nil {
			return nil, fmt.Errorf("corrupt keyset record: %w", err)
		}
		if onDisk.Tenant != "" {
			out = append(out, onDisk.Tenant)
		}
	}
	return out, nil
}

type fileKeysetRecord struct {
	Tenant string
	StoredKeyset
}

func writeRecordFile(f *os.File, tenant string, record StoredKeyset) error {
	b, err := json.Marshal(fileKeysetRecord{Tenant: tenant, StoredKeyset: record})
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func writeRecordAtomic(path string, tenant string, record StoredKeyset) error {
	b, err := json.Marshal(fileKeysetRecord{Tenant: tenant, StoredKeyset: record})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".keyset-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = df.Close() }()
	return df.Sync()
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
	if _, ok := s.data[tenant]; ok {
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
		return errInvalidArgument("keyset revision conflict")
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
