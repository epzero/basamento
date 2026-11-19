package gate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCheckConflictMarkersCatchesEachForm fails on every marker shape
// qa-gate.md names: the opening and closing lines git writes (seven or more
// '<' or '>', a space, a label) and a line of seven or more '='.
func TestCheckConflictMarkersCatchesEachForm(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantLine int
	}{
		{"opening HEAD", "package main\n<<<<<<< HEAD\n", 2},
		{"opening branch", "<<<<<<< origin/main\n", 1},
		{"opening revision and subject", "<<<<<<< 0123abc (subject)\n", 1},
		{"closing", "> > text\n>>>>>>> main\n", 2},
		{"separator", "a\n=======\n", 2},
		{"eight opens", "<<<<<<<< HEAD\n", 1},
		{"eight closes", ">>>>>>>> HEAD\n", 1},
		{"carriage return", "a\r\n=======\r\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t)
			commitBytes(t, root, "conflict.txt", []byte(tc.body))
			errs, err := CheckConflictMarkers(root)
			if err != nil {
				t.Fatal(err)
			}
			want := "conflict.txt:" + strconv.Itoa(tc.wantLine) + ": merge conflict marker"
			if !hasErr(errs, want) {
				t.Fatalf("marker %q must fail with %q, got %v", tc.body, want, errs)
			}
			if len(errs) != 1 {
				t.Fatalf("one marker line must give one error, got %v", errs)
			}
		})
	}
}

// TestCheckConflictMarkersCountsEveryLine is the whole conflict block: the
// opening, separator and closing lines each fail, the diff3 base line needs no
// rule of its own.
func TestCheckConflictMarkersCountsEveryLine(t *testing.T) {
	root := gitRepo(t)
	commitBytes(t, root, "block.txt", []byte("<<<<<<< HEAD\nours\n||||||| base\nbase\n=======\ntheirs\n>>>>>>> main\n"))
	errs, err := CheckConflictMarkers(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"block.txt:1:", "block.txt:5:", "block.txt:7:"} {
		if !hasErr(errs, want) {
			t.Errorf("missing %q in %v", want, errs)
		}
	}
	if hasErr(errs, "block.txt:3:") {
		t.Errorf("a diff3 base line needs no rule of its own: %v", errs)
	}
}

// TestCheckConflictMarkersAllowsNonMarkers covers the two shapes qa-gate.md
// says pass: a Markdown setext underline and any marker inside a running line.
// A bare run with no label is not a marker either: the rule needs the space
// and label git writes.
func TestCheckConflictMarkersAllowsNonMarkers(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		body string
	}{
		{"markdown setext", "doc.md", "Title\n=======\n"},
		{"markdown setext long extension", "doc.markdown", "Title\n=======\n"},
		{"markdown setext after line with spaces", "doc.md", "Title  \n=======\n"},
		{"opening inside a sentence", "doc.md", "a <<<<<<< HEAD in a sentence\n"},
		{"separator inside a sentence", "doc.md", "quote ======= here\n"},
		{"indented opening", "doc.md", "    <<<<<<< HEAD\n"},
		{"indented separator", "doc.md", "    =======\n"},
		{"indented closing", "doc.md", "    >>>>>>> main\n"},
		{"bare opening run", "doc.md", "<<<<<<<\n"},
		{"bare closing run", "doc.md", ">>>>>>>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t)
			commitBytes(t, root, tc.file, []byte(tc.body))
			errs, err := CheckConflictMarkers(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(errs) != 0 {
				t.Fatalf("%q must pass, got %v", tc.body, errs)
			}
		})
	}
}

// TestCheckConflictMarkersSeparatorOutsideSetextFails pins the exception's
// edges: only a separator directly under a non-blank line, in Markdown, passes.
func TestCheckConflictMarkersSeparatorOutsideSetextFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		body string
	}{
		{"markdown under a blank line", "doc.md", "Text\n\n=======\n"},
		{"markdown at the start", "doc.md", "=======\nText\n"},
		{"non-markdown under a non-blank line", "notes.txt", "Title\n=======\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t)
			commitBytes(t, root, tc.file, []byte(tc.body))
			errs, err := CheckConflictMarkers(root)
			if err != nil {
				t.Fatal(err)
			}
			if !hasErr(errs, "merge conflict marker") {
				t.Fatalf("%q must fail, got %v", tc.body, errs)
			}
		})
	}
}

// TestCheckConflictMarkersSkipsBinaryAndSymlink covers the two type skips:
// git's binary test (a NUL in the first 8000 bytes) and a tracked symlink,
// which is not followed out of the tree.
func TestCheckConflictMarkersSkipsBinaryAndSymlink(t *testing.T) {
	root := gitRepo(t)
	commitBytes(t, root, "binary.dat", append([]byte("\x00"), []byte("\n=======\n")...))
	commitBytes(t, root, "late-nul.dat", append([]byte(strings.Repeat("a", 8000)+"\n=======\n"), 0))

	target := filepath.Join(root, "untracked-target.txt")
	if err := os.WriteFile(target, []byte("<<<<<<< HEAD\n=======\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("untracked-target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "link.txt")
	mustGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "link")

	errs, err := CheckConflictMarkers(root)
	if err != nil {
		t.Fatal(err)
	}
	if hasErr(errs, "binary.dat") {
		t.Errorf("a NUL in the first 8000 bytes makes the file binary: %v", errs)
	}
	if hasErr(errs, "link.txt") {
		t.Errorf("a tracked symlink must not be read: %v", errs)
	}
	if !hasErr(errs, "late-nul.dat:2:") {
		t.Errorf("a NUL after the first 8000 bytes is not git's binary test: %v", errs)
	}
}

// TestCheckConflictMarkersReadsOnlyTrackedTextFiles covers the scope: every
// tracked file counts (data files included), an untracked one does not, and
// the hits come back sorted by path and line.
func TestCheckConflictMarkersReadsOnlyTrackedTextFiles(t *testing.T) {
	root := gitRepo(t)
	commitBytes(t, root, "b/changes.feature", []byte("Feature: f\n=======\n"))
	commitBytes(t, root, "a/go.sum.txt", []byte("<<<<<<< HEAD\n"))
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("<<<<<<< HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	errs, err := CheckConflictMarkers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 2 {
		t.Fatalf("want two hits, got %v", errs)
	}
	if !strings.HasPrefix(errs[0], "a/go.sum.txt:1:") || !strings.HasPrefix(errs[1], "b/changes.feature:2:") {
		t.Fatalf("hits must be sorted by path then line, got %v", errs)
	}
	if hasErr(errs, "untracked.txt") {
		t.Errorf("an untracked file counts only once it is added: %v", errs)
	}
}

// TestConflictMarkerAllowlist pins the one approved escape hatch: a glob
// relative to the repository root, matched by the gate's own glob syntax.
func TestConflictMarkerAllowlist(t *testing.T) {
	globs := []string{"internal/gate/testdata/conflict*"}
	if !conflictMarkerAllowedBy("internal/gate/testdata/conflict.md", globs) {
		t.Error("an allowlisted file must pass")
	}
	if conflictMarkerAllowedBy("internal/gate/testdata/other.md", globs) {
		t.Error("a file outside the allowlist must fail")
	}
	if conflictMarkerAllowedBy("specs/x/conflict.md", nil) {
		t.Error("no path is exempt when the allowlist is empty")
	}
}

// TestHygieneFailsOnConflictMarker proves the check runs inside gate hygiene,
// which is what blocks make gate.
func TestHygieneFailsOnConflictMarker(t *testing.T) {
	root := gitRepo(t)
	commitBytes(t, root, "docs/note.md", []byte("text\n>>>>>>> main\n"))
	errs, err := Hygiene(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasErr(errs, "docs/note.md:2: merge conflict marker") {
		t.Fatalf("Hygiene must report the marker, got %v", errs)
	}
}
