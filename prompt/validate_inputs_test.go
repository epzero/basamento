package prompt

import (
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestValidateInputsDeterministicDetail(t *testing.T) {
	t.Helper()
	tpl := &Template{
		ID: "pair", Version: "1",
		Inputs: []Input{
			{Name: "alpha", Trust: TrustTrusted},
			{Name: "beta", Trust: TrustTrusted},
			{Name: "gamma", Trust: TrustTrusted},
		},
		Segments: []Segment{{Name: "ctx", Kind: KindDynamic, Budget: 100, Text: `{{alpha}}`}},
	}
	reg := NewRegistry()
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		_, err := reg.Assemble("pair@1", map[string]string{"beta": "b", "gamma": "c"}, nil)
		if err == nil {
			t.Fatal("expected missing input")
		}
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatal(err)
		}
		var perr *core.Error
		if !errors.As(err, &perr) {
			t.Fatal(err)
		}
		if perr.Details["input"] != "alpha" {
			t.Fatalf("missing: iter %d got input %q", i, perr.Details["input"])
		}
	}
	for i := 0; i < 50; i++ {
		_, err := reg.Assemble("pair@1", map[string]string{
			"alpha": "a", "beta": "b", "gamma": "c", "extra": "z",
		}, nil)
		if err == nil {
			t.Fatal("expected undeclared input")
		}
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatal(err)
		}
		var perr *core.Error
		if !errors.As(err, &perr) {
			t.Fatal(err)
		}
		if perr.Details["input"] != "extra" {
			t.Fatalf("undeclared: iter %d got input %q", i, perr.Details["input"])
		}
	}
}
