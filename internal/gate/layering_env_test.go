package gate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeLayeringGoEnv(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "replaces_gowork_goflags",
			in:   []string{"HOME=/tmp", "GOWORK=/x", "GOFLAGS=-overlay=/y"},
			want: []string{"HOME=/tmp", "GOWORK=off", "GOFLAGS=-mod=readonly"},
		},
		{
			name: "absent_vars_added",
			in:   []string{"PATH=/bin"},
			want: []string{"PATH=/bin", "GOWORK=off", "GOFLAGS=-mod=readonly"},
		},
		{
			name: "duplicate_goflags_stripped",
			in:   []string{"GOFLAGS=-tags=foo", "OTHER=1", "GOFLAGS=-mod=mod"},
			want: []string{"OTHER=1", "GOWORK=off", "GOFLAGS=-mod=readonly"},
		},
		{
			name: "goenv_passthrough",
			in:   []string{"GOENV=/custom/goenv"},
			want: []string{"GOENV=/custom/goenv", "GOWORK=off", "GOFLAGS=-mod=readonly"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLayeringGoEnv(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLayeringGoListIgnoresOverlay(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":  "package a\n\nimport _ \"example.com/mod/high/b\"\n",
		"high/b/b.go": "package b\n",
	})
	active := filepath.Join(root, "low/a/a.go")
	replacement := filepath.Join(t.TempDir(), "clean.go")
	if err := os.WriteFile(replacement, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(t.TempDir(), "overlay.json")
	ov := map[string]any{
		"Replace": map[string]string{
			active: replacement,
		},
	}
	b, err := json.Marshal(ov)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlayPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-overlay="+overlayPath)
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := "low/a (layer low) may not import high/b (layer high)"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want [%q]", got, want)
	}
}

func TestLayeringGoEnvFileCannotLeakOverlay(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":  "package a\n\nimport _ \"example.com/mod/high/b\"\n",
		"high/b/b.go": "package b\n",
	})
	active := filepath.Join(root, "low/a/a.go")
	replacement := filepath.Join(t.TempDir(), "clean.go")
	if err := os.WriteFile(replacement, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(t.TempDir(), "overlay.json")
	ov := map[string]any{
		"Replace": map[string]string{
			active: replacement,
		},
	}
	b, err := json.Marshal(ov)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlayPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	goenvFile := filepath.Join(t.TempDir(), "goenv")
	overlayFlag := "-overlay=" + overlayPath
	if f, err := os.Create(goenvFile); err != nil {
		t.Fatal(err)
	} else if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	writeEnv := exec.Command("go", "env", "-w", "GOFLAGS="+overlayFlag)
	writeEnv.Env = append(minimalGoEnv(t), "GOENV="+goenvFile)
	if out, err := writeEnv.CombinedOutput(); err != nil {
		t.Fatalf("go env -w: %v: %s", err, out)
	}
	if prev, had := os.LookupEnv("GOFLAGS"); had {
		t.Cleanup(func() {
			_ = os.Setenv("GOFLAGS", prev)
		})
	} else {
		t.Cleanup(func() {
			_ = os.Unsetenv("GOFLAGS")
		})
	}
	if err := os.Unsetenv("GOFLAGS"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goenvFile)
	verifyEnv := minimalGoEnv(t)
	verifyEnv = append(verifyEnv, "GOENV="+goenvFile)
	cmd := exec.Command("go", "env", "GOFLAGS")
	cmd.Env = verifyEnv
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOFLAGS: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != overlayFlag {
		t.Fatalf("with GOENV file and no process GOFLAGS, go env GOFLAGS=%q want %q", got, overlayFlag)
	}
	var childFlags bool
	for _, e := range layeringGoEnv() {
		if e == "GOFLAGS=-mod=readonly" {
			childFlags = true
			break
		}
	}
	if !childFlags {
		t.Fatal("layeringGoEnv must set GOFLAGS=-mod=readonly on child processes")
	}
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := "low/a (layer low) may not import high/b (layer high)"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want [%q]", got, want)
	}
}

func minimalGoEnv(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, key := range []string{"PATH", "HOME", "GOPATH", "GOROOT", "GOMODCACHE", "GOTOOLCHAIN", "GOPROXY"} {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

func TestLayeringGoEnvUsedByGoList(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	got := sanitizeLayeringGoEnv(os.Environ())
	var flags string
	for _, e := range got {
		if strings.HasPrefix(e, "GOFLAGS=") {
			flags = e
		}
	}
	if flags != "GOFLAGS=-mod=readonly" {
		t.Fatalf("sanitize: got %q, want GOFLAGS=-mod=readonly", flags)
	}
}

func TestNestedModuleProbeIgnoresAmbientGoFlags(t *testing.T) {
	okMod := filepath.Join(t.TempDir(), "ok.mod")
	if err := os.WriteFile(okMod, []byte("module ok.example\n\ngo 1.22.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := layeringModule(t, map[string]string{
		"low/a/a.go":      "package a\n",
		"low/evil/go.mod": "not a real mod\n",
		"low/evil/x.go":   "package x\n",
	})
	evilDir := filepath.Join(root, "low/evil")
	t.Setenv("GOFLAGS", "-modfile="+okMod)
	probe := exec.Command("go", "list", "-m")
	probe.Dir = evilDir
	probe.Env = os.Environ()
	if err := probe.Run(); err != nil {
		t.Fatalf("precondition: unsanitized go list -m in low/evil with -modfile must succeed, got %v", err)
	}
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := goModCannotLoadMsg("low/evil/go.mod", "low/evil")
	if !containsString(got, want) {
		t.Fatalf("got %v, want containing %q", got, want)
	}
}
