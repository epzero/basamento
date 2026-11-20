package gate

import (
	"encoding/json"
	"os"
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
			want: []string{"HOME=/tmp", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOENV=off"},
		},
		{
			name: "absent_vars_added",
			in:   []string{"PATH=/bin"},
			want: []string{"PATH=/bin", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOENV=off"},
		},
		{
			name: "duplicate_goflags_stripped",
			in:   []string{"GOFLAGS=-tags=foo", "OTHER=1", "GOFLAGS=-mod=mod"},
			want: []string{"OTHER=1", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOENV=off"},
		},
		{
			name: "goenv_cleared",
			in:   []string{"GOENV=/custom/goenv"},
			want: []string{"GOWORK=off", "GOFLAGS=-mod=readonly", "GOENV=off"},
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
