package gate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fmtGitEnv forces default core.quotePath=true so the test does not depend on the
// host's git config; the old fmt recipe (without -z) still mis-handles non-ASCII paths.
// The fixed recipe keeps -c core.quotePath=false beside -z to match gitPathsUnquoted.
func fmtGitEnv(t *testing.T) []string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "nogitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return []string{
		"GIT_CONFIG_GLOBAL=" + cfg,
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.quotePath",
		"GIT_CONFIG_VALUE_0=true",
	}
}

// fmtScratchRepo initializes an empty git repository in dir.
func fmtScratchRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fmtGitEnv(t)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// runMakeFmt runs make fmt against the repository Makefile with a pinned git environment.
func runMakeFmt(t *testing.T, repoDir string) (output string, exitErr error) {
	t.Helper()
	makefile := filepath.Join(makefileRoot(t), "Makefile")
	cmd := exec.Command("make", "-s", "-f", makefile, "fmt")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), fmtGitEnv(t)...)
	out, err := cmd.CombinedOutput()
	output = string(out)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return output, ee
		}
		t.Fatalf("make fmt: %v: %s", err, out)
	}
	return output, nil
}

// TestMakefileFmt checks that make fmt catches unformatted or invalid Go files regardless of path.
func TestMakefileFmt(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string // path relative to repo root -> content
		wantErr bool
		wantOut []string // substrings that must appear in output when wantErr is true
		noOut   []string // substrings that must not appear in output when wantErr is false
	}{
		{
			name: "unformatted non-ASCII path",
			files: map[string]string{
				"\u00e9.go": "package main\n\nfunc main(){}\n",
			},
			wantErr: true,
			wantOut: []string{"gofmt needed:", "\u00e9.go"},
		},
		{
			name: "formatted non-ASCII path",
			files: map[string]string{
				"\u00e9.go": "package main\n\nfunc main() {}\n",
			},
			wantErr: false,
		},
		{
			name: "unformatted path with space",
			files: map[string]string{
				"my file.go": "package main\n\nfunc main(){}\n",
			},
			wantErr: true,
			wantOut: []string{"gofmt needed:", "my file.go"},
		},
		{
			name: "syntax error",
			files: map[string]string{
				"bad.go": "package main\n\nfunc main() {\n",
			},
			wantErr: true,
			wantOut: []string{"bad.go"},
		},
		{
			name: "formatted ASCII only",
			files: map[string]string{
				"a.go": "package main\n\nfunc main() {}\n",
			},
			wantErr: false,
			noOut:   []string{"gofmt needed:"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			fmtScratchRepo(t, repo)
			for rel, content := range tc.files {
				path := filepath.Join(repo, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			out, exitErr := runMakeFmt(t, repo)
			if tc.wantErr {
				if exitErr == nil {
					t.Fatalf("expected non-zero exit, output:\n%s", out)
				}
				for _, sub := range tc.wantOut {
					if !strings.Contains(out, sub) {
						t.Fatalf("output missing %q:\n%s", sub, out)
					}
				}
				return
			}
			if exitErr != nil {
				t.Fatalf("expected success, exit=%v output:\n%s", exitErr, out)
			}
			for _, sub := range tc.noOut {
				if strings.Contains(out, sub) {
					t.Fatalf("output must not contain %q:\n%s", sub, out)
				}
			}
		})
	}
}
