package schema

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/epzero/basamento/core"
)

type EmbedNamedBytes []byte

type EmbedLevel int

type EmbedLabel string

type EmbedTags []string

type EmbedChan chan int

type EmbedInner struct {
	A int `json:"a"`
}

type EmbedHidden struct {
	H int `json:"h"`
}

type EmbedInnerTagged struct {
	A int `json:"a"`
}

type EmbedBase struct {
	ID int `json:"id"`
}

type EmbedDerived struct {
	EmbedBase
	ID int `json:"id"`
}

type EmbedStringer interface {
	String() string
}

type EmbedStringerValue int

func (EmbedStringerValue) String() string { return "s" }

func compileGeneratedForValue(t *testing.T, v any) *Compiled {
	t.Helper()
	s, err := ForType(reflect.TypeOf(v))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s.Raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func marshalValidateAccept(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c := compileGeneratedForValue(t, v)
	if err := c.Validate(b); err != nil {
		t.Fatalf("instance %s: %v", b, err)
	}
}

func TestGenerateMarshalValidateEmbeddedFields(t *testing.T) {
	t.Parallel()

	type namedBytesWrap struct {
		EmbedNamedBytes
		Note string `json:"note"`
	}
	type levelWrap struct {
		EmbedLevel
		Note string `json:"note"`
	}
	type labelWrap struct {
		EmbedLabel
		Note string `json:"note"`
	}
	type tagsWrap struct {
		EmbedTags
		Note string `json:"note"`
	}
	type levelPtrSet struct {
		*EmbedLevel
		Note string `json:"note"`
	}
	type levelPtrNil struct {
		*EmbedLevel
		Note string `json:"note"`
	}
	type namedBytesPtrSet struct {
		*EmbedNamedBytes
		Note string `json:"note"`
	}
	type namedBytesPtrNil struct {
		*EmbedNamedBytes
		Note string `json:"note"`
	}
	type rawWrap struct {
		json.RawMessage
		Note string `json:"note"`
	}
	type stringerNil struct {
		EmbedStringer
		Note string `json:"note"`
	}
	type stringerSet struct {
		EmbedStringer
		Note string `json:"note"`
	}
	type innerWrap struct {
		EmbedInner
		Note string `json:"note"`
	}
	type innerPtrNil struct {
		*EmbedInner
		Note string `json:"note"`
	}
	type innerPtrSet struct {
		*EmbedInner
		Note string `json:"note"`
	}
	type hiddenWrap struct {
		EmbedHidden `json:"-"`
		Note        string `json:"note"`
	}
	type taggedWrap struct {
		EmbedInnerTagged `json:"x"`
		Note             string `json:"note"`
	}
	type shadowWrap struct {
		EmbedDerived
		Title string `json:"title"`
	}

	cases := []struct {
		name string
		v    any
	}{
		{"named_bytes", namedBytesWrap{EmbedNamedBytes: []byte("hi")}},
		{"level", levelWrap{EmbedLevel: 1}},
		{"label", labelWrap{EmbedLabel: "x"}},
		{"tags", tagsWrap{EmbedTags: []string{"a"}}},
		{"level_ptr_set", levelPtrSet{EmbedLevel: ptr(EmbedLevel(2))}},
		{"level_ptr_nil", levelPtrNil{}},
		{"named_bytes_ptr_set", namedBytesPtrSet{EmbedNamedBytes: ptr(EmbedNamedBytes("hi"))}},
		{"named_bytes_ptr_nil", namedBytesPtrNil{}},
		{"raw_inlined", rawWrap{RawMessage: json.RawMessage(`{"note":"v"}`)}},
		{"raw_null_root", rawWrap{}},
		{"raw_extra", rawWrap{RawMessage: json.RawMessage(`{"extra":1}`), Note: "y"}},
		{"stringer_nil", stringerNil{}},
		{"stringer_set", stringerSet{EmbedStringer: EmbedStringerValue(0)}},
		{"embedded_struct", innerWrap{EmbedInner: EmbedInner{A: 1}, Note: "n"}},
		{"embedded_struct_zero", innerWrap{}},
		{"embedded_ptr_nil", innerPtrNil{}},
		{"embedded_ptr_set", innerPtrSet{EmbedInner: &EmbedInner{A: 2}}},
		{"embedded_hidden", hiddenWrap{EmbedHidden: EmbedHidden{H: 1}}},
		{"embedded_tagged", taggedWrap{EmbedInnerTagged: EmbedInnerTagged{A: 1}}},
		{"shadowing", shadowWrap{EmbedDerived: EmbedDerived{EmbedBase: EmbedBase{ID: 1}, ID: 2}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marshalValidateAccept(t, tc.v)
		})
	}
}

func TestGenerateEmbeddedChanFailsClosed(t *testing.T) {
	type chWrap struct {
		EmbedChan
		Note string `json:"note"`
	}
	_, err := ForType(reflect.TypeOf(chWrap{}))
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
}

func ptr[T any](v T) *T {
	return &v
}
