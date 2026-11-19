package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

// Types with their own encoding, used by the tests below. They mirror the
// scenario fixtures: a text encoding on the value, a JSON encoding on the
// value, and one that only the pointer has.

type testTextStamp struct{}

func (testTextStamp) MarshalText() ([]byte, error) { return []byte("stamp"), nil }

type testJSONStamp struct{}

func (testJSONStamp) MarshalJSON() ([]byte, error) { return []byte("[1,2]"), nil }

type testPointerStamp struct{}

func (*testPointerStamp) MarshalJSON() ([]byte, error) { return []byte("[1]"), nil }

type testTextString string

func (s testTextString) MarshalText() ([]byte, error) { return []byte("stamp" + string(s)), nil }

type testTextBytes []byte

func (testTextBytes) MarshalText() ([]byte, error) { return []byte("bin"), nil }

type testPointerTextBytes []byte

func (*testPointerTextBytes) MarshalText() ([]byte, error) { return []byte("bin"), nil }

type testTextArray [4]byte

func (testTextArray) MarshalText() ([]byte, error) { return []byte("arr"), nil }

type testIntKeyed map[int]string

type testTextKey string

func (k testTextKey) MarshalText() ([]byte, error) { return []byte("S" + string(k)), nil }

type testTextKeyed map[testTextKey]string

type testFloatKeyed map[float64]string

func TestTextMarshalerSchemaIsString(t *testing.T) {
	sample := struct {
		V testTextStamp `json:"v"`
	}{}
	validateGenerated(t, sample, []string{`{"v":"stamp"}`}, []string{`{"v":null}`, `{"v":{}}`, `{"v":1}`})
}

func TestJSONMarshalerSchemaAdmitsEveryValue(t *testing.T) {
	sample := struct {
		V testJSONStamp `json:"v"`
	}{}
	validateGenerated(t, sample,
		[]string{`{"v":[1,2]}`, `{"v":null}`, `{"v":{}}`, `{"v":"x"}`, `{"v":3}`}, nil)
}

func TestPointerOnlyEncodingSchemaAdmitsEveryValue(t *testing.T) {
	sample := struct {
		V testPointerStamp `json:"v"`
	}{}
	validateGenerated(t, sample,
		[]string{`{"v":[1]}`, `{"v":null}`, `{"v":{}}`, `{"v":"x"}`}, nil)
}

func TestTimeAndNumberKeepTheirEncodingSchemas(t *testing.T) {
	timeSample := struct {
		V time.Time `json:"v"`
	}{V: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	validateGenerated(t, timeSample,
		[]string{`{"v":"2020-01-02T03:04:05Z"}`}, []string{`{"v":1}`, `{"v":null}`})

	numberSample := struct {
		V json.Number `json:"v"`
	}{V: "1.5"}
	validateGenerated(t, numberSample,
		[]string{`{"v":1}`, `{"v":1.5}`},
		[]string{`{"v":"1"}`, `{"v":null}`, `{"v":true}`})
}

func TestByteContainerWithOwnEncodingIsNotBase64OrAnIntegerArray(t *testing.T) {
	textBytesSample := struct {
		V testTextBytes `json:"v"`
	}{}
	validateGenerated(t, textBytesSample,
		[]string{`{"v":"bin"}`}, []string{`{"v":null}`, `{"v":[98,105,110]}`})

	textArraySample := struct {
		V testTextArray `json:"v"`
	}{}
	validateGenerated(t, textArraySample,
		[]string{`{"v":"arr"}`}, []string{`{"v":null}`, `{"v":[1,2,3,4]}`})
}

func TestPointerOnlyEncodingOnByteContainerAdmitsEveryValue(t *testing.T) {
	sample := struct {
		V testPointerTextBytes `json:"v"`
	}{}
	validateGenerated(t, sample,
		[]string{`{"v":null}`, `{"v":"bin"}`, `{"v":[1,2]}`}, nil)
}

func TestByteSliceWithoutOwnEncodingStaysBase64(t *testing.T) {
	sample := struct {
		V []byte `json:"v"`
	}{V: []byte("hi")}
	validateGenerated(t, sample,
		[]string{`{"v":null}`, `{"v":"aGk="}`}, []string{`{"v":[104,105]}`})
}

func TestMapKeysEncodingJSONWrites(t *testing.T) {
	intKeyed := struct {
		V testIntKeyed `json:"v"`
	}{V: testIntKeyed{7: "x"}}
	validateGenerated(t, intKeyed,
		[]string{`{"v":{"7":"x"}}`, `{"v":null}`}, []string{`{"v":{"7":1}}`})

	textKeyed := struct {
		V testTextKeyed `json:"v"`
	}{V: testTextKeyed{"0": "x"}}
	validateGenerated(t, textKeyed,
		[]string{`{"v":{"S0":"x"}}`, `{"v":null}`}, []string{`{"v":{"S0":1}}`})
}

func TestMapKeyWithoutJSONTextRejectedWithFieldAndKey(t *testing.T) {
	_, err := ForType(reflect.TypeFor[struct {
		M testFloatKeyed `json:"m"`
	}]())
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Details == nil {
		t.Fatalf("details %+v", err)
	}
	if pe.Details["field"] != "M" || pe.Details["key"] != "float64" {
		t.Fatalf("details %+v", pe.Details)
	}
}

func TestEmbeddedFieldWithOwnEncodingIsGenerated(t *testing.T) {
	sample := struct{ json.Number }{}
	b, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"Number":0}` {
		t.Fatalf("marshal %s", b)
	}
	validateGenerated(t, sample, []string{string(b), `{"Number":1.5}`}, []string{`{"Number":"1"}`})
}

func TestStringOptionOnNumbersAndBooleans(t *testing.T) {
	tests := []struct {
		name   string
		sample any
		accept []string
		reject []string
	}{
		{
			name: "int",
			sample: struct {
				V int `json:"v,string"`
			}{},
			accept: []string{`{"v":"0"}`, `{"v":"-4"}`},
			reject: []string{`{"v":0}`, `{"v":"04"}`},
		},
		{
			name: "int64",
			sample: struct {
				V int64 `json:"v,string"`
			}{},
			accept: []string{`{"v":"9223372036854775807"}`},
			reject: []string{`{"v":0}`},
		},
		{
			name: "uint",
			sample: struct {
				V uint `json:"v,string"`
			}{},
			accept: []string{`{"v":"7"}`},
			reject: []string{`{"v":"-1"}`},
		},
		{
			name: "float64",
			sample: struct {
				V float64 `json:"v,string"`
			}{},
			accept: []string{`{"v":"0"}`, `{"v":"1.5"}`, `{"v":"0.000001"}`, `{"v":"1e-7"}`, `{"v":"1e+21"}`},
			reject: []string{`{"v":1.5}`},
		},
		{
			name: "bool",
			sample: struct {
				V bool `json:"v,string"`
			}{},
			accept: []string{`{"v":"true"}`, `{"v":"false"}`},
			reject: []string{`{"v":false}`, `{"v":"TRUE"}`},
		},
		{
			name: "number",
			sample: struct {
				V json.Number `json:"v,string"`
			}{},
			accept: []string{`{"v":"3"}`, `{"v":"1.5"}`, `{"v":"1e+21"}`},
			reject: []string{`{"v":3}`, `{"v":"x"}`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := compileForType(t, tc.sample)
			for _, inst := range tc.accept {
				if err := c.Validate(json.RawMessage(inst)); err != nil {
					t.Fatalf("accept %s: %v", inst, err)
				}
			}
			for _, inst := range tc.reject {
				if err := c.Validate(json.RawMessage(inst)); err == nil {
					t.Fatalf("reject %s: accepted", inst)
				}
			}
		})
	}
}

func TestStringOptionOnStringsHoldsTheQuotedString(t *testing.T) {
	sample := struct {
		V string `json:"v,string"`
	}{}
	validateGenerated(t, sample,
		[]string{`{"v":"\"plain\""}`, `{"v":"\"a\\\"b\""}`, `{"v":"\"\""}`},
		[]string{`{"v":"plain"}`, `{"v":null}`, `{"v":1}`})
}

func TestStringOptionOnPointerAdmitsNull(t *testing.T) {
	sample := struct {
		V *int `json:"v,string"`
	}{}
	validateGenerated(t, sample,
		[]string{`{"v":null}`, `{"v":"0"}`}, []string{`{"v":0}`})
}

func TestStringOptionOnNamedPointerKeepsItsOwnEncoding(t *testing.T) {
	type namedPointer *int
	value := 3
	sample := struct {
		V namedPointer `json:"v,string"`
	}{V: namedPointer(&value)}
	b, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"v":3}` {
		t.Fatalf("marshal %s", b)
	}
	validateGenerated(t, sample,
		[]string{string(b), `{"v":null}`}, []string{`{"v":"3"}`})
}

func TestStringOptionKeepsOwnEncoding(t *testing.T) {
	sample := struct {
		V testTextString `json:"v,string"`
	}{}
	validateGenerated(t, sample,
		[]string{`{"v":"stamp"}`}, []string{`{"v":null}`, `{"v":{}}`})
}

func TestTextEncodingWinsInsideContainers(t *testing.T) {
	slice := struct {
		V []testTextStamp `json:"v"`
	}{}
	validateGenerated(t, slice,
		[]string{`{"v":["stamp"]}`, `{"v":null}`}, []string{`{"v":[{}]}`, `{"v":[null]}`})

	mapValue := struct {
		V map[string]testTextStamp `json:"v"`
	}{}
	validateGenerated(t, mapValue,
		[]string{`{"v":{"k":"stamp"}}`, `{"v":null}`}, []string{`{"v":{"k":{}}}`})
}
