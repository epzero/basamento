package gate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChangedFilesKeepsNonASCIIPaths(t *testing.T) {
	root := gitRepo(t)
	commitBytes(t, root, "go.mod", []byte("module x\n\ngo 1.26.11\n"))
	rel := "nested/é/note.txt"
	commitBytes(t, root, rel, []byte("a\n"))
	base := gitHead(t, root)
	if err := os.WriteFile(filepath.Join(root, rel), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := ChangedFiles(root, base, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if f == rel {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing %q in %v", rel, files)
	}
}
