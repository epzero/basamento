package schema

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func compileForType(t *testing.T, sample any) *Compiled {
	t.Helper()
	s, err := ForType(reflect.TypeOf(sample))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s.Raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func validateGenerated(t *testing.T, sample any, accept, reject []string) {
	t.Helper()
	c := compileForType(t, sample)
	for _, inst := range accept {
		if err := c.Validate(json.RawMessage(inst)); err != nil {
			t.Fatalf("accept %s: %v", inst, err)
		}
	}
	for _, inst := range reject {
		if err := c.Validate(json.RawMessage(inst)); err == nil {
			t.Fatalf("reject %s: accepted", inst)
		} else if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("reject %s: code %v", inst, core.CodeOf(err))
		}
	}
}

type generateValidateCase struct {
	name        string
	constructor func() any
	acceptJSON  []string
	rejectJSON  []string
	marshalZero bool
}

func TestGenerateValidationRoundTrip(t *testing.T) {
	type Inner struct {
		A int `json:"a"`
	}
	type Embed struct {
		Inner
		Y int `json:"y"`
	}
	type Bytes []byte

	cases := []generateValidateCase{
		{
			name: "root_struct",
			constructor: func() any {
				return struct {
					X int `json:"x"`
				}{}
			},
			marshalZero: true,
			rejectJSON:  []string{`null`},
		},
		{
			name: "value_struct_field",
			constructor: func() any {
				return struct {
					In Inner `json:"in"`
				}{}
			},
			acceptJSON: []string{`{"in":{"a":1}}`},
			rejectJSON: []string{`{"in":null}`, `{"in":1}`},
		},
		{
			name: "pointer_struct_field",
			constructor: func() any {
				return struct {
					In *Inner `json:"in"`
				}{}
			},
			acceptJSON:  []string{`{"in":null}`},
			marshalZero: true,
		},
		{
			name: "slice_null",
			constructor: func() any {
				return struct {
					T []string `json:"t"`
				}{}
			},
			acceptJSON:  []string{`{"t":null}`, `{"t":["a"]}`},
			marshalZero: true,
		},
		{
			name: "fixed_byte_array",
			constructor: func() any {
				return struct {
					A [2]byte `json:"a"`
				}{A: [2]byte{1, 2}}
			},
			marshalZero: true,
			rejectJSON:  []string{`{"a":null}`},
		},
		{
			name: "byte_slice",
			constructor: func() any {
				return struct {
					B []byte `json:"b"`
				}{B: []byte("hi")}
			},
			marshalZero: true,
			rejectJSON:  []string{`{"b":[1,2]}`},
		},
		{
			name: "named_byte_slice",
			constructor: func() any {
				return struct {
					B Bytes `json:"b"`
				}{B: Bytes("hi")}
			},
			marshalZero: true,
		},
		{
			name: "raw_message",
			constructor: func() any {
				return struct {
					R json.RawMessage `json:"r"`
				}{}
			},
			acceptJSON: []string{
				`{"r":null}`,
				`{"r":1}`,
				`{"r":"x"}`,
				`{"r":[]}`,
				`{"r":{}}`,
			},
			marshalZero: true,
		},
		{
			name: "map_string_any",
			constructor: func() any {
				return struct {
					M map[string]any `json:"m"`
				}{}
			},
			acceptJSON:  []string{`{"m":null}`, `{"m":{"k":1}}`},
			marshalZero: true,
		},
		{
			name: "map_string_int",
			constructor: func() any {
				return struct {
					M map[string]int `json:"m"`
				}{M: map[string]int{"a": 1}}
			},
			acceptJSON:  []string{`{"m":null}`},
			marshalZero: true,
		},
		{
			name: "map_of_slices",
			constructor: func() any {
				return struct {
					G map[string][]string `json:"g"`
				}{}
			},
			acceptJSON:  []string{`{"g":null}`, `{"g":{"x":null}}`},
			marshalZero: true,
		},
		{
			name: "nested_map_depth",
			constructor: func() any {
				type Leaf struct {
					Scores map[string]int `json:"scores"`
				}
				return struct {
					Leaf Leaf `json:"leaf"`
				}{}
			},
			acceptJSON:  []string{`{"leaf":{"scores":null}}`},
			marshalZero: true,
		},
		{
			name: "slice_nested_in_struct",
			constructor: func() any {
				type Inner struct {
					Tags []string `json:"tags"`
				}
				return struct {
					In Inner `json:"in"`
				}{}
			},
			acceptJSON:  []string{`{"in":{"tags":null}}`},
			marshalZero: true,
		},
		{
			name: "slice_of_struct",
			constructor: func() any {
				type Row struct {
					Tags []string `json:"tags"`
				}
				return struct {
					Rows []Row `json:"rows"`
				}{}
			},
			acceptJSON:  []string{`{"rows":[{"tags":null}]}`},
			marshalZero: true,
			rejectJSON:  []string{`{"rows":[null]}`},
		},
		{
			name: "embedded_struct",
			constructor: func() any {
				return Embed{Y: 2, Inner: Inner{A: 1}}
			},
			marshalZero: true,
		},
		{
			name: "any_field",
			constructor: func() any {
				return struct {
					V any `json:"v"`
				}{V: map[string]int{"a": 1}}
			},
			marshalZero: true,
		},
		{
			name: "time_field",
			constructor: func() any {
				return struct {
					T time.Time `json:"t"`
				}{T: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
			},
			marshalZero: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sample := tc.constructor()
			var accept []string
			accept = append(accept, tc.acceptJSON...)
			if tc.marshalZero {
				b, err := json.Marshal(sample)
				if err != nil {
					t.Fatal(err)
				}
				accept = append(accept, string(b))
			}
			validateGenerated(t, sample, accept, tc.rejectJSON)
		})
	}
}

func TestGeneratedRecursiveTypeCycleRejected(t *testing.T) {
	type Node struct {
		Next *Node `json:"next,omitempty"`
	}
	_, err := ForType(reflect.TypeOf(Node{}))
	if err == nil {
		t.Fatal("want cycle error from generator")
	}
}

func TestMapStringAnyNilMarshalRoundTrip(t *testing.T) {
	type S struct {
		M map[string]any `json:"m"`
	}
	sample := S{}
	b, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"m":null}` {
		t.Fatalf("marshal: %s", b)
	}
	validateGenerated(t, sample, []string{string(b)}, nil)
}

func TestByteSliceBase64RoundTrip(t *testing.T) {
	type S struct {
		B []byte `json:"b"`
	}
	sample := S{B: []byte("hi")}
	b, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"b":"aGk="}` {
		t.Fatalf("marshal: %s", b)
	}
	validateGenerated(t, sample, []string{string(b)}, nil)
}

func TestOpenMapObject(t *testing.T) {
	tests := []struct {
		ap   any
		want bool
	}{
		{false, false},
		{true, true},
		{map[string]any{"type": "integer"}, true},
		{nil, false},
	}
	for _, tt := range tests {
		pm := map[string]any{"additionalProperties": tt.ap}
		if got := openMapObject(pm); got != tt.want {
			t.Fatalf("ap %#v: got %v want %v", tt.ap, got, tt.want)
		}
	}
}
