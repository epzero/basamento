package expr

import "testing"

func TestFloat64IntegralInt64Shared(t *testing.T) {
	if _, ok := float64IntegralInt64(3.0); !ok {
		t.Fatal("want in-range integral")
	}
	if float64IntegralOutsideInt64(3.0) {
		t.Fatal("3.0 is not outside range")
	}
	if _, ok := float64IntegralInt64(float64(1 << 63)); ok {
		t.Fatal("want reject 2^63 float")
	}
	if !float64IntegralOutsideInt64(float64(1 << 63)) {
		t.Fatal("want outside range integral")
	}
}

func TestBindAndJsonTypeOfAgreeOnRange(t *testing.T) {
	f := float64(9007199254740991) // 2^53−1, exact in float64
	if _, err := bindInt(f); err != nil {
		t.Fatalf("in-range integral float should bind: %v", err)
	}
	if jsonTypeOf(f) != "integer" {
		t.Fatalf("jsonTypeOf %q", jsonTypeOf(f))
	}
	if jsonTypeOf(float64(1<<63)) != "integer" {
		t.Fatalf("outside-range integral is integer schema type")
	}
	if _, ok := float64IntegralInt64(float64(1 << 63)); ok {
		t.Fatal("2^63 float must not bind as int64")
	}
}
