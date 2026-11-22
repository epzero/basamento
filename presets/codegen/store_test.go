package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestRecordSurvivesReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "scores.versions.json")
	src := DefaultSources()
	reg := NewRegistry([]string{"plan-modules", "iterate-minimal"})
	reg.Add("v1", src, map[string]int{"plan-modules": 82, "iterate-minimal": 75})
	reg.AcceptInitial("v1")
	reg.Add("v2", src, map[string]int{"plan-modules": 85, "iterate-minimal": 74})
	if v := reg.Propose("v2"); v.Code != core.CodeConflict {
		t.Fatalf("v2 = %s/%s, want conflict", v.Status, v.Code)
	}
	if err := SaveRegistry(path, reg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRegistry(path, []string{"plan-modules", "iterate-minimal"})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Current != "v1" {
		t.Fatalf("current = %q, want v1", loaded.Current)
	}
	v2 := loaded.Get("v2")
	if v2 == nil || v2.Status != StatusRejected || v2.Code != core.CodeConflict || v2.Case != "iterate-minimal" {
		t.Fatalf("v2 = %+v, want rejected conflict iterate-minimal", v2)
	}
	// A re-run does not silently re-propose a recorded rejection.
	if got := loaded.Propose("v2"); got.Status != StatusRejected || got.Code != core.CodeConflict {
		t.Fatalf("re-proposed v2 = %s/%s, want rejected conflict", got.Status, got.Code)
	}
}

func TestLoadRegistryMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	reg, err := LoadRegistry(filepath.Join(t.TempDir(), "none.versions.json"), []string{"plan-modules"})
	if err != nil {
		t.Fatal(err)
	}
	if reg.Current != "" || len(reg.Versions) != 0 {
		t.Fatalf("registry = %+v, want empty", reg)
	}
}

func TestLoadRegistryRejectsAMalformedRecord(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "scores.versions.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path, nil); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestRecordPathIsNextToTheScores(t *testing.T) {
	t.Parallel()
	if got, want := RecordPath("/tmp/scores.json"), "/tmp/scores.versions.json"; got != want {
		t.Fatalf("RecordPath = %q, want %q", got, want)
	}
}

func TestSaveRegistryWritesAPrivateFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "scores.versions.json")
	reg := NewRegistry(nil)
	if err := SaveRegistry(path, reg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("record mode %o, want no group or other access", perm)
	}
}
