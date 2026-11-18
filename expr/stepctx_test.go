package expr

import (
	"testing"

	"github.com/itchyny/gojq"
)

func TestRepeatStepPolls(t *testing.T) {
	q, err := gojq.Parse("repeat(1)")
	if err != nil {
		t.Fatal(err)
	}
	code, err := gojq.Compile(q)
	if err != nil {
		t.Fatal(err)
	}
	ctx := newStepContext(10_000_000)
	iter := code.RunWithContext(ctx, nil)
	v, ok := iter.Next()
	if !ok {
		t.Fatal("no first value")
	}
	t.Logf("first=%v", v)
}
