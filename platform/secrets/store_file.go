package secrets

import (
	"context"
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
	s := &FileStore{dir: dir}
	s.cleanupTemps()
	return s, nil
}

func (s *FileStore) cleanupTemps() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".keyset-") {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

func (s *FileStore) path(tenant string) (string, error) {
	if err := ValidateTenant(tenant); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(tenant))
	name := hex.EncodeToString(sum[:]) + ".json"
	return filepath.Join(s.dir, name), nil
}

func (s *FileStore) Get(ctx context.Context, tenant string) (StoredKeyset, bool, error) {
	if err := ctx.Err(); err != nil {
		return StoredKeyset{}, false, err
	}
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

func (s *FileStore) CreateIfAbsent(ctx context.Context, tenant string, record StoredKeyset) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(tenant)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return s.writeRecordLocked(p, tenant, record, false)
}

func (s *FileStore) CompareAndSwap(ctx context.Context, tenant string, expectRevision uint64, record StoredKeyset) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(tenant)
	if err != nil {
		return err
	}
	cur, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return errConflict(msgKeysetRevisionConflict)
	}
	if err != nil {
		return err
	}
	var old fileKeysetRecord
	if err := json.Unmarshal(cur, &old); err != nil {
		return fmt.Errorf("corrupt keyset record: %w", err)
	}
	if old.Revision != expectRevision {
		return errConflict(msgKeysetRevisionConflict)
	}
	_, err = s.writeRecordLocked(p, tenant, record, true)
	return err
}

func (s *FileStore) Count(ctx context.Context) (int, error) {
	ents, err := s.Tenants(ctx)
	return len(ents), err
}

func (s *FileStore) Tenants(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

func (s *FileStore) writeRecordLocked(path, tenant string, record StoredKeyset, replace bool) (bool, error) {
	b, err := json.Marshal(fileKeysetRecord{Tenant: tenant, StoredKeyset: record})
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".keyset-*.tmp")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return false, err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if replace {
		if err := os.Rename(tmp, path); err != nil {
			return false, err
		}
		if err := syncDir(dir); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		if err := os.Rename(tmp, path); err != nil {
			if errors.Is(err, os.ErrExist) {
				return false, nil
			}
			return false, err
		}
	} else {
		_ = os.Remove(tmp)
	}
	if err := syncDir(dir); err != nil {
		return false, err
	}
	return true, nil
}

func syncDir(dir string) error {
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = df.Close() }()
	return df.Sync()
}
