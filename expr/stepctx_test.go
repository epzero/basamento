package expr

import (
	"testing"

	"github.com/itchyny/gojq"
)

func TestStepLimitCancelsRepeat(t *testing.T) {
	q, err := gojq.Parse("repeat(1)")
	if err != nil {
		t.Fatal(err)
	}
	code, err := gojq.Compile(q)
	if err != nil {
		t.Fatal(err)
	}
	ctx := newStepContext(100)
	iter := code.RunWithContext(ctx, nil)
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
		if ctx.limitExceeded() {
			return
		}
	}
	t.Fatal("want step limit cancellation")
}