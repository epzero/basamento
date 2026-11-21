package sqltest

import (
	"fmt"
	"strings"
	"testing"
)

func TestRecordSkipRejectsUnknownReason(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if !strings.Contains(fmt.Sprint(r), "unknown skip") {
			t.Fatalf("panic %v", r)
		}
	}()
	RecordSkipRun("other-reason")
}
