package blob

import (
	"io"
	"math"
	"strings"
	"testing"
)

func TestReadLimitMaxInt64(t *testing.T) {
	t.Parallel()
	lim := readLimit(math.MaxInt64)
	if lim != math.MaxInt64 {
		t.Fatalf("readLimit(MaxInt64) = %d", lim)
	}
	r := io.LimitReader(strings.NewReader("x"), lim)
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
}

func TestReadLimitDefault(t *testing.T) {
	t.Parallel()
	if readLimit(100) != 101 {
		t.Fatal("want max+1")
	}
}
