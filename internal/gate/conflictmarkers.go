package gate

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// conflictMarkerAllowlist names the tracked files that may legitimately hold a
// merge conflict marker, as globs relative to the repository root. A path gets
// here only through a reviewed change to the gate, never through a directive
// inside the file, an exempt extension or an exempt directory (qa-gate.md
// § Conflict markers). No path is exempt today: the rule's own tests build
// their repositories in t.TempDir, which this repository does not track.
var conflictMarkerAllowlist []string

// binarySniffLen is git's own binary probe: a NUL in the first 8000 bytes
// means the file is binary, so no conflict marker is looked for.
const binarySniffLen = 8000

// CheckConflictMarkers reports a merge conflict marker at the start of a line
// in any tracked text file. A marker is an opening line of seven or more '<'
// or a closing line of seven or more '>', each followed by a space and the
// label git writes, or a line of nothing but seven or more '='. A diff3 base
// line needs no rule of its own: the opening line above it already fails.
// A line of only '=' directly under a non-blank line in Markdown is a setext
// heading underline and passes; a marker inside a running line passes too,
// since only column one counts.
//
// The whole tracked tree is read, not the diff, so a marker inherited from the
// base branch fails the next gate. Tracked symbolic links and binary files are
// skipped; an untracked file is covered once it is added. Hits are sorted, one
// "path:line" per marker, so the same commit always reports the same list.
func CheckConflictMarkers(root string) ([]string, error) {
	files, err := gitPathsUnquoted(root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var errs []string
	for _, rel := range files {
		if conflictMarkerAllowedBy(rel, conflictMarkerAllowlist) {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			// Deleted in the worktree but still in the index: there is no
			// content to read.
			continue
		}
		if err != nil {
			return nil, err
		}
		// A tracked symlink is not followed: its target is tracked on its own
		// and following it could leave the tree. Any other non-regular entry
		// (a submodule gitlink) has no text to read.
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		if isBinaryContent(data) {
			continue
		}
		errs = append(errs, conflictMarkersInFile(rel, data)...)
	}
	sort.Strings(errs)
	return errs, nil
}

// conflictMarkerAllowedBy reports whether rel matches one of the reviewed
// globs. An empty list exempts nothing.
func conflictMarkerAllowedBy(rel string, globs []string) bool {
	for _, g := range globs {
		if globRegexp(g).MatchString(rel) {
			return true
		}
	}
	return false
}

func isBinaryContent(data []byte) bool {
	if len(data) > binarySniffLen {
		data = data[:binarySniffLen]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func conflictMarkersInFile(rel string, data []byte) []string {
	lines := strings.Split(string(data), "\n")
	markdown := isMarkdownPath(rel)
	var errs []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if isConflictSeparator(line) {
			if markdown && i > 0 && strings.TrimSpace(lines[i-1]) != "" {
				// A setext heading underline, not a separator.
				continue
			}
			errs = append(errs, conflictMarkerError(rel, i+1))
			continue
		}
		if isConflictEdge(line, '<') || isConflictEdge(line, '>') {
			errs = append(errs, conflictMarkerError(rel, i+1))
		}
	}
	return errs
}

// isConflictEdge reports whether line opens or closes a conflict: seven or more
// of c at column one, then the space and non-empty label git writes.
func isConflictEdge(line string, c byte) bool {
	n := 0
	for n < len(line) && line[n] == c {
		n++
	}
	if n < 7 || n >= len(line) || line[n] != ' ' {
		return false
	}
	return strings.TrimSpace(line[n+1:]) != ""
}

// isConflictSeparator reports whether line is nothing but seven or more '='.
func isConflictSeparator(line string) bool {
	if len(line) < 7 {
		return false
	}
	for i := 0; i < len(line); i++ {
		if line[i] != '=' {
			return false
		}
	}
	return true
}

func isMarkdownPath(rel string) bool {
	ext := strings.ToLower(path.Ext(rel))
	return ext == ".md" || ext == ".markdown"
}

func conflictMarkerError(rel string, line int) string {
	return fmt.Sprintf("%s:%d: merge conflict marker; resolve the conflict and remove the marker line (qa-gate.md § Conflict markers)", rel, line)
}
