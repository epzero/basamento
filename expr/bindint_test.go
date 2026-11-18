package expr

import "testing"

func TestBindIntRejectsOutOfRangeFloat(t *testing.T) {
	if _, err := bindInt(float64(1 << 63)); err == nil {
		t.Fatal("want error for 2^63 float")
	}
}
