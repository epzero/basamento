package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
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