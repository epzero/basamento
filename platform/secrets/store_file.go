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
	"syscall"
)

// linkOrRename links temp to final when hard links work; otherwise renames.
var linkOrRename = func(tempPath, finalPath string) error {
	if err := os.Link(tempPath, finalPath); err != nil {
		if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.ENOTSUP) {
			return os.Rename(tempPath, finalPath)
		}
		return err
	}
	return os.Remove(tempPath)
}

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
	if _, err := os.Stat(p); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	tmp, err := s.writeTempRecord(tenant, record)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := linkOrRename(tmp, p); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := syncDir(s.dir); err != nil {
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

func (s *FileStore) writeTempRecord(tenant string, record StoredKeyset) (string, error) {
	b, err := json.Marshal(fileKeysetRecord{Tenant: tenant, StoredKeyset: record})
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.dir, ".keyset-*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return name, nil
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
	if _, err := syncDir(dir); err != nil {
		return err
	}
	return nil
}

func syncDir(dir string) (bool, error) {
	df, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = df.Close() }()
	return true, df.Sync()
}
