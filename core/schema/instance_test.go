package schema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestValidateRejectsHugeExponentNoPanic(t *testing.T) {
	raw := json.RawMessage(`{"n":1e1000001}`)
	c, err := Compile(json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(raw); err == nil {
		t.Fatal("want error")
	} else if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
}

func TestDecodeInstanceRejectsDeepNesting(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("[", maxNestingDepth+1))
	b.WriteString("1")
	b.WriteString(strings.Repeat("]", maxNestingDepth+1))
	_, err := decodeInstance([]byte(b.String()))
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeInstanceTruncatedInvalidArgument(t *testing.T) {
	_, err := decodeInstance([]byte(`{"a":`))
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
}
