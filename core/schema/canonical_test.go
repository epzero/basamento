package schema

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestCanonicalizeWhitespaceAndSort(t *testing.T) {
	in := []byte(`{
        "version": 3,
        "flow": "codegen.feature",
        "edges": [ {"to": "plan", "from": "START"}, {"to": "END", "from": "plan"} ]
      }`)
	want := `{"edges":[{"from":"START","to":"plan"},{"from":"plan","to":"END"}],"flow":"codegen.feature","version":3}`
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalizeNumberShortest(t *testing.T) {
	got, err := Canonicalize([]byte(`1.0`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1" {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalizeNumberExamples(t *testing.T) {
	// RFC 8785 appendix B number examples.
	cases := map[string]string{
		"0.000001":              "0.000001",
		"1E-7":                  "1e-7",
		"1.0":                   "1",
		"1.500":                 "1.5",
		"2.22":                  "2.22",
		"1e+1":                  "10",
		"-0.0":                  "0",
		"-0.0000000001":         "-1e-10",
		"0.0000000001":          "1e-10",
		"100000000000000000000": "100000000000000000000",
		"1e20":                  "100000000000000000000",
		"9.999999999999999e20":  "999999999999999900000",
		"1e21":                  "1e+21",
		"1.5e21":                "1.5e+21",
		"9007199254740992":      "9007199254740992",
		"-1.50":                 "-1.5",
		"0.1":                   "0.1",
		"10.0":                  "10",
		"0.10":                  "0.1",
	}
	for in, want := range cases {
		got, err := Canonicalize([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(got) != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestCanonicalizeRefusesExponentNonExactNumber(t *testing.T) {
	for _, n := range []string{
		"9007199254740993e0",
		"0.10000000000000000001e0",
		"9223372036854775808e0",
	} {
		_, err := Canonicalize([]byte(`{"n":` + n + `}`))
		if err == nil {
			t.Fatalf("%s: want error", n)
		}
	}
}

// A long but bounded literal that differs from its canonical form far below
// float64's ~17 significant digits must be rejected, not rounded (REQ-SCHEMA-035).
func TestCanonicalizeRejectsPrecisionLossBelowFloat256(t *testing.T) {
	text := "0.1" + strings.Repeat("0", 79) + "1" // 82 significant mantissa digits
	_, err := Canonicalize([]byte(`{"n":` + text + `}`))
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}

func TestCanonicalizeRejectsDeepNesting(t *testing.T) {
	for _, prefix := range []string{
		strings.Repeat("[", maxNestingDepth+1) + "1" + strings.Repeat("]", maxNestingDepth+1),
		strings.Repeat(`{"a":`, maxNestingDepth+1) + `1` + strings.Repeat("}", maxNestingDepth+1),
	} {
		_, err := Canonicalize([]byte(prefix))
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("prefix depth: err=%v", err)
		}
	}
}

func TestCanonicalizeRejectsUnescapedControlBytes(t *testing.T) {
	for _, b := range []byte{0x00, 0x01, 0x08, 0x0b, 0x1f} {
		doc := []byte{'"', b, '"'}
		_, err := Canonicalize(doc)
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("byte 0x%02x: err=%v", b, err)
		}
	}
	for _, doc := range []string{`"ok"`, `"a\u0001"`, `"\n"`, `"\t"`, `"\u001f"`} {
		if _, err := Canonicalize([]byte(doc)); err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
	}
}

func TestCanonicalizePrefixNoPanic(t *testing.T) {
	doc := `{"a":1,"b":["x"]}`
	for i := 1; i < len(doc); i++ {
		func() {
			defer func() {
				if recover() != nil {
					t.Fatalf("panic on prefix %q", doc[:i])
				}
			}()
			_, err := Canonicalize([]byte(doc[:i]))
			if err == nil {
				t.Fatalf("prefix %q: want error", doc[:i])
			}
		}()
	}
}

func TestCanonicalizeAcceptsFixedDecimalForms(t *testing.T) {
	for _, n := range []string{"10.0", "0.0", "-0.0", "0.10"} {
		got, err := Canonicalize([]byte(n))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		t.Logf("%s -> %s", n, got)
	}
}

func TestCanonicalizeRejectsNonJSONNumberSyntax(t *testing.T) {
	for _, n := range []string{"+1", "01"} {
		_, err := Canonicalize([]byte(n))
		if err == nil {
			t.Fatalf("%s: want error", n)
		}
	}
}

func TestCanonicalizeScientificFixedBoundary(t *testing.T) {
	got, err := Canonicalize([]byte(`1e16`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "10000000000000000" {
		t.Fatalf("1e16 should be fixed notation, got %q", got)
	}
	got2, err := Canonicalize([]byte(`9007199254740994`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got2), "e") {
		t.Fatalf("9007199254740994 should be fixed notation, got %q", got2)
	}
}

func TestCanonicalizeRefusesNonExactNumber(t *testing.T) {
	for _, n := range []string{
		"9007199254740993",
		"12345678901234567890",
		"0.10000000000000000001",
		"1e400",
		"1e-400",
	} {
		_, err := Canonicalize([]byte(`{"run":{"n":` + n + `}}`))
		if err == nil {
			t.Fatalf("%s: want error", n)
		}
		if ErrLocation(err) != "/run/n" {
			t.Fatalf("%s: location %q", n, ErrLocation(err))
		}
	}
}
