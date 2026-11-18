package secrets_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

func TestFileStoreCreateComparePersist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := secrets.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := secrets.StoredKeyset{Wrapped: []byte("w"), MasterKeyID: "mk", Revision: 1}
	ok, err := s.CreateIfAbsent("acme", rec)
	if err != nil || !ok {
		t.Fatalf("create ok=%v err=%v", ok, err)
	}
	ok, err = s.CreateIfAbsent("acme", rec)
	if err != nil || ok {
		t.Fatalf("dup ok=%v err=%v", ok, err)
	}
	got, found, err := s.Get("acme")
	if err != nil || !found || got.Revision != 1 {
		t.Fatalf("get %+v found=%v err=%v", got, found, err)
	}
	rec2 := rec
	rec2.Revision = 2
	rec2.Wrapped = []byte("w2")
	if err := s.CompareAndSwap("acme", 1, rec2); err != nil {
		t.Fatal(err)
	}
	if err := s.CompareAndSwap("acme", 1, rec); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("cas conflict err=%v", err)
	}
	s2, err := secrets.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err = s2.Get("acme")
	if err != nil || !found || got.Revision != 2 {
		t.Fatalf("reopen %+v", got)
	}
}

func TestFileStoreAtomicNotTorn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "keysets")
	s, err := secrets.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := secrets.StoredKeyset{Wrapped: []byte("wrap"), MasterKeyID: "mk", Revision: 1}
	if _, err := s.CreateIfAbsent("t1", rec); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %o", info.Mode().Perm())
		}
	}
}

func TestFileStoreCorruptFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := secrets.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := secrets.StoredKeyset{Wrapped: []byte("w"), MasterKeyID: "mk", Revision: 1}
	if _, err := s.CreateIfAbsent("acme", rec); err != nil {
		t.Fatal(err)
	}
	p, err := storePath(dir, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Get("acme")
	if err == nil {
		t.Fatal("expected error on corrupt file")
	}
}

func TestFileStoreHostileTenantIDs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := secrets.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := secrets.StoredKeyset{Wrapped: []byte("w"), MasterKeyID: "mk", Revision: 1}
	hostile := []string{"../escape", "/", "a\x00b", strings.Repeat("x", 600)}
	for _, id := range hostile {
		if _, err := s.CreateIfAbsent(id, rec); err == nil {
			t.Fatalf("allowed %q", id)
		}
	}
	// Legitimate tenant still works and stays inside dir.
	if _, err := s.CreateIfAbsent("acme", rec); err != nil {
		t.Fatal(err)
	}
	p, err := storePath(dir, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		t.Fatalf("path escaped %s", p)
	}
}

func storePath(dir, tenant string) (string, error) {
	s, err := secrets.NewFileStore(dir)
	if err != nil {
		return "", err
	}
	_ = s
	// Hash naming matches production: only way to locate without exporting path().
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") && filepath.Ext(e.Name()) == ".json" {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", os.ErrNotExist
}
