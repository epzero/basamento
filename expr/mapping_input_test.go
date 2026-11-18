package expr

import (
	"encoding/json"
	"fmt"
	"math/big"
	"testing"
)

func TestNormalizeMappingInputLargeInts(t *testing.T) {
	cases := []struct {
		in   json.Number
		want any
	}{
		{"9007199254740993", int(9007199254740993)},
		{"9223372036854775807", int(9223372036854775807)},
		{"18446744073709551615", new(big.Int).SetUint64(18446744073709551615)},
	}
	for _, tc := range cases {
		got := normalizeMappingInputValue(tc.in)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("%s: got %v (%T) want %v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}

func TestMappingPreservesLargeInt(t *testing.T) {
	m, err := CompileMapping(`.`, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := json.Number("9007199254740993")
	out, err := m.Eval(n, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(out) != "9007199254740993" {
		t.Fatalf("got %v", out)
	}
}
