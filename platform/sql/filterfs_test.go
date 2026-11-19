package sql

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestFilterFSRejectsHighVersionOpen(t *testing.T) {
	dir := t.TempDir()
	writeMig := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("--"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeMig("00001_first.sql")
	writeMig("00010_tenth.sql")
	root := os.DirFS(dir)
	filt, err := FilterMigrations(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(filt, "00010_tenth.sql"); err == nil {
		t.Fatal("expected missing high version file")
	}
	if _, err := fs.ReadFile(filt, "../00010_tenth.sql"); err == nil {
		t.Fatal("expected invalid path rejection")
	}
}
