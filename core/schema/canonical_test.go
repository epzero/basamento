package schema

import "testing"

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
	cases := map[string]string{
		"0.000001":         "0.000001",
		"1E-7":             "1e-7",
		"0.1":              "0.1",
		"9007199254740992": "9007199254740992",
		"-1.50":            "-1.5",
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
