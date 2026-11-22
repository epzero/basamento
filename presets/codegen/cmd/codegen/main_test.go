package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	codegen "github.com/epzero/basamento/presets/codegen"
)

func writeScores(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDecision(t *testing.T) {
	t.Parallel()
	for _, decision := range []string{"approved", "rejected"} {
		if err := validateDecision(decision); err != nil {
			t.Fatalf("%q: %v", decision, err)
		}
	}
	for _, decision := range []string{"", "maybe"} {
		if err := validateDecision(decision); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%q: err = %v, want invalid_argument", decision, err)
		}
	}
}

// S10: eval prints the scores file's values, not the embedded recording.
func TestWriteEvalMeansUsesTheFile(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	recorded := map[string]map[string]int{
		"plan-modules": {"module_coverage": 42, "prompt_quality": 43},
	}
	if err := writeEvalMeans(&out, recorded); err != nil {
		t.Fatal(err)
	}
	want := "plan-modules module_coverage 42.00\nplan-modules prompt_quality 43.00\n"
	if out.String() != want {
		t.Fatalf("output %q, want %q", out.String(), want)
	}
	if err := writeEvalMeans(&out, map[string]map[string]int{}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("empty file err = %v, want invalid_argument", err)
	}
}

// S11: propose and resolve share a durable record next to the scores file.
func TestProposeAndResolvePersistTheRecord(t *testing.T) {
	t.Parallel()
	scores := filepath.Join(t.TempDir(), "scores.json")
	writeScores(t, scores, `{
  "v1": {"plan-modules": 82, "iterate-minimal": 75},
  "v2": {"plan-modules": 85, "iterate-minimal": 80}
}`)
	var out bytes.Buffer
	if err := proposeTo(&out, []string{"--version", "v2", "--current", "v1", "--scores", scores}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "review gate open") {
		t.Fatalf("propose output %q, want an open review gate", out.String())
	}
	out.Reset()
	if err := resolveTo(&out, []string{"--version", "v2", "--decision", "approved", "--scores", scores}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "accepted") {
		t.Fatalf("resolve output %q, want accepted", out.String())
	}
	reg, err := codegen.LoadRegistry(codegen.RecordPath(scores), codegen.Suite())
	if err != nil {
		t.Fatal(err)
	}
	if reg.Current != "v2" || reg.Get("v2").Status != codegen.StatusAccepted {
		t.Fatalf("record current %q v2 %s, want v2 accepted", reg.Current, reg.Get("v2").Status)
	}
	if reg.Get("v1").Status != codegen.StatusSuperseded {
		t.Fatalf("v1 = %s, want superseded", reg.Get("v1").Status)
	}
}

// S11: a recorded rejection is not erased by a re-run with better scores.
func TestProposeDoesNotReproposeARejectedVersion(t *testing.T) {
	t.Parallel()
	scores := filepath.Join(t.TempDir(), "scores.json")
	writeScores(t, scores, `{
  "v1": {"plan-modules": 82, "iterate-minimal": 75},
  "v2": {"plan-modules": 85, "iterate-minimal": 74}
}`)
	var out bytes.Buffer
	if err := proposeTo(&out, []string{"--version", "v2", "--current", "v1", "--scores", scores}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "code conflict") {
		t.Fatalf("propose output %q, want conflict", out.String())
	}
	writeScores(t, scores, `{
  "v1": {"plan-modules": 82, "iterate-minimal": 75},
  "v2": {"plan-modules": 95, "iterate-minimal": 95}
}`)
	out.Reset()
	if err := proposeTo(&out, []string{"--version", "v2", "--current", "v1", "--scores", scores}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "code conflict") || strings.Contains(out.String(), "review gate open") {
		t.Fatalf("re-run output %q, want the recorded rejection", out.String())
	}
}

func TestProposeRequiresAKnownVersion(t *testing.T) {
	t.Parallel()
	scores := filepath.Join(t.TempDir(), "scores.json")
	writeScores(t, scores, `{"v1": {"plan-modules": 82, "iterate-minimal": 75}}`)
	var out bytes.Buffer
	if err := proposeTo(&out, []string{"--scores", scores}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing --version err = %v, want invalid_argument", err)
	}
	if err := proposeTo(&out, []string{"--version", "v9", "--scores", scores}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("unknown --version err = %v, want invalid_argument", err)
	}
}

func TestResolveRequiresADecisionAndARecordedVersion(t *testing.T) {
	t.Parallel()
	scores := filepath.Join(t.TempDir(), "scores.json")
	writeScores(t, scores, `{"v2": {"plan-modules": 85, "iterate-minimal": 80}}`)
	var out bytes.Buffer
	if err := resolveTo(&out, []string{"--version", "v2", "--scores", scores}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing --decision err = %v, want invalid_argument", err)
	}
	if err := resolveTo(&out, []string{"--version", "v2", "--decision", "maybe", "--scores", scores}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("unknown --decision err = %v, want invalid_argument", err)
	}
	if err := resolveTo(&out, []string{"--version", "v2", "--decision", "approved", "--scores", scores}); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("unrecorded version err = %v, want not_found", err)
	}
}
