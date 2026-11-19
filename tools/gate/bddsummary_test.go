package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/epzero/basamento/internal/gate"
)

func TestPrintBDDSkipsBranches(t *testing.T) {
	dir := t.TempDir()
	gateDir := filepath.Join(dir, ".gate")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(gateDir, "bdd.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		json string
		want string
	}{
		{`{"outcome":"pass","skip_runs":{}}`, "gate: scenario runs skipped: 0\n"},
		{`{"outcome":"pass","skip_runs":{"postgres-unavailable":3}}`, "gate: scenario runs skipped: 3 (postgres-unavailable: 3)\n"},
		{`{"outcome":"pass","skip_runs":{"postgres-unavailable":1,"zzz-future":2}}`, "gate: scenario runs skipped: 3 (postgres-unavailable: 1, zzz-future: 2)\n"},
	}
	for _, tc := range cases {
		write(tc.json)
		out := captureToolStdout(t, func() {
			if err := printBDDSkips(dir); err != nil {
				t.Fatal(err)
			}
		})
		if out != tc.want {
			t.Fatalf("got %q want %q", out, tc.want)
		}
	}
}

func TestPrintBDDSkipsFromPackageSubdirectory(t *testing.T) {
	modRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(modRoot, "go.mod"), []byte("module x\n\ngo 1.26.11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gateDir := filepath.Join(modRoot, ".gate")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"outcome":"pass","skip_runs":{"postgres-unavailable":2}}`
	if err := os.WriteFile(filepath.Join(gateDir, "bdd.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(modRoot, "internal", "bdd")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	modRootFound, err := gate.RepoRoot(cwd)
	if err != nil {
		t.Fatal(err)
	}
	out := captureToolStdout(t, func() {
		if err := printBDDSkips(modRootFound); err != nil {
			t.Fatal(err)
		}
	})
	want := "gate: scenario runs skipped: 2 (postgres-unavailable: 2)\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func captureToolStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String()
}
