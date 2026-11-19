package conformance_test

import (
	"os"
	"strings"
	"testing"
)

// bddSubprocessEnv drops inherited BDD/conformance variables, then appends overrides.
func bddSubprocessEnv(extra ...string) []string {
	prefixes := []string{
		"BDD_TAGS=",
		"BDD_TAGS_AND=",
		"BDD_PATHS=",
		"BLOB_CONFORMANCE_BACKEND=",
	}
	var out []string
	for _, e := range os.Environ() {
		drop := false
		for _, p := range prefixes {
			if strings.HasPrefix(e, p) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return append(out, extra...)
}

func TestBDDSubprocessEnvStripsInherited(t *testing.T) {
	t.Setenv("BDD_TAGS", "@wip")
	t.Setenv("BDD_TAGS_AND", "~@planned")
	t.Setenv("BDD_PATHS", "specs/other")
	t.Setenv("BLOB_CONFORMANCE_BACKEND", "file")

	got := bddSubprocessEnv(
		"BDD_TAGS=",
		"BLOB_CONFORMANCE_BACKEND=s3",
		"BDD_PATHS=specs/blob",
	)

	want := map[string]string{
		"BDD_TAGS":                 "",
		"BLOB_CONFORMANCE_BACKEND": "s3",
		"BDD_PATHS":                "specs/blob",
	}
	for key, val := range want {
		n := 0
		var seen string
		for _, e := range got {
			if !strings.HasPrefix(e, key+"=") {
				continue
			}
			n++
			seen = strings.TrimPrefix(e, key+"=")
		}
		if n != 1 {
			t.Fatalf("%s: want one entry, got %d in %v", key, n, got)
		}
		if seen != val {
			t.Fatalf("%s: got %q want %q", key, seen, val)
		}
	}
}
