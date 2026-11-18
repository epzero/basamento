package schema

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func mustCompileUniqueItemsNumber(t *testing.T) *Compiled {
	t.Helper()
	c, err := Compile(json.RawMessage(`{"type":"array","items":{"type":"number"},"uniqueItems":true}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidateUniqueItemsAtExponentBound(t *testing.T) {
	const n = 1000
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%de%d", i+1, maxNumberExponent)
	}
	b.WriteByte(']')
	if err := mustCompileUniqueItemsNumber(t).Validate(json.RawMessage(b.String())); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsExponentBeyondBound(t *testing.T) {
	c, err := Compile(json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{
		fmt.Sprintf("1e%d", maxNumberExponent+1),
		"1e1000001",
	} {
		err := c.Validate(json.RawMessage(`{"n":` + n + `}`))
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%s: err=%v", n, err)
		}
	}
}

func TestValidateRejectsUniqueItemsAttackExponents(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 1000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "1e%d", 900000+i)
	}
	b.WriteByte(']')
	err := mustCompileUniqueItemsNumber(t).Validate(json.RawMessage(b.String()))
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateRejectsLongMantissa(t *testing.T) {
	c, err := Compile(json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	digits := strings.Repeat("1", maxNumberMantissaDigits+1)
	err = c.Validate(json.RawMessage(`{"n":` + digits + `}`))
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateAcceptsGenerousNumberRange(t *testing.T) {
	c, err := Compile(json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []string{
		`{"n":9223372036854775807}`,
		`{"n":-9223372036854775808}`,
		`{"n":18446744073709551615}`,
		`{"n":1e308}`,
	} {
		if err := c.Validate(json.RawMessage(inst)); err != nil {
			t.Fatalf("%s: %v", inst, err)
		}
	}
}

func TestCanonicalizeStillRejectsBeyondDouble(t *testing.T) {
	_, err := Canonicalize([]byte(`1e400`))
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
}