package expr

import (
	"encoding/json"
	"testing"
)

func TestBindIntRejectsOutOfRangeFloat(t *testing.T) {
	if _, err := bindInt(float64(1 << 63)); err == nil {
		t.Fatal("want error for 2^63 float")
	}
}

func TestBindIntAcceptsJSONNumberThreePointZero(t *testing.T) {
	if _, err := bindInt(json.Number("3.0")); err != nil {
		t.Fatalf("want integral json.Number: %v", err)
	}
}
