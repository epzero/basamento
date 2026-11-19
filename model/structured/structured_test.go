package structured

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

const answerSchema = `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`

func mustSpec(t *testing.T, supported ...Rung) *Spec {
	t.Helper()
	spec, err := NewSpec(json.RawMessage(answerSchema), supported)
	if err != nil {
		t.Fatalf("NewSpec: %v", err)
	}
	return spec
}

func replyWith(t *testing.T, content string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"role": "assistant", "content": content})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLadderIsCanonical(t *testing.T) {
	got := Ladder()
	want := []Rung{RungNativeSchema, RungJSONMode, RungForcedTool}
	if len(got) != len(want) {
		t.Fatalf("ladder = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ladder = %v, want %v", got, want)
		}
	}
}

func TestNewSpecOrdersSupportedRungs(t *testing.T) {
	spec := mustSpec(t, RungForcedTool, RungNativeSchema)
	want := []Rung{RungNativeSchema, RungForcedTool}
	got := spec.Supported()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("supported = %v, want %v", got, want)
	}
}

func TestParseRungs(t *testing.T) {
	got, err := ParseRungs("native schema, JSON mode, forced tool call")
	if err != nil {
		t.Fatalf("ParseRungs: %v", err)
	}
	want := []Rung{RungNativeSchema, RungJSONMode, RungForcedTool}
	if len(got) != len(want) {
		t.Fatalf("rungs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rungs = %v, want %v", got, want)
		}
	}
	none, err := ParseRungs("")
	if err != nil || len(none) != 0 {
		t.Fatalf("empty rungs = %v, %v", none, err)
	}
	if _, err := ParseRungs("magic"); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("unknown rung error = %v, want invalid_argument", err)
	}
}

func TestValidate(t *testing.T) {
	spec := mustSpec(t, RungJSONMode)
	tests := []struct {
		name    string
		content string
		ok      bool
		canon   string
	}{
		{name: "holds", content: `{"answer": 7}`, ok: true, canon: `{"answer":7}`},
		{name: "wrong type", content: `{"answer": "seven"}`, ok: false},
		{name: "missing field", content: `{}`, ok: false},
		{name: "extra field", content: `{"answer": 7, "note": "x"}`, ok: false},
		{name: "prose", content: "Here: {\"answer\": 7}", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := spec.Validate(replyWith(t, tc.content))
			if ok != tc.ok {
				t.Fatalf("Validate ok = %v, want %v", ok, tc.ok)
			}
			if ok && string(got) != tc.canon {
				t.Fatalf("Validate = %s, want %s", got, tc.canon)
			}
		})
	}
}

func TestValidateToolCallAnswer(t *testing.T) {
	spec := mustSpec(t, RungForcedTool)
	message := json.RawMessage(`{"role":"assistant","tool_calls":[{"function":{"name":"structured_output","arguments":"{\"answer\": 7}"}}]}`)
	got, ok := spec.Validate(message)
	if !ok || string(got) != `{"answer":7}` {
		t.Fatalf("Validate = %s, %v", got, ok)
	}
}

func TestRepairStripsFenceProseAndTrailingComma(t *testing.T) {
	spec := mustSpec(t, RungJSONMode)
	message := replyWith(t, "```json\n{\"answer\": 7,}\n```\nHope this helps!")
	got, ok := spec.Repair(message)
	if !ok {
		t.Fatal("Repair did not satisfy the schema")
	}
	if string(got) != `{"answer":7}` {
		t.Fatalf("Repair = %s, want {\"answer\":7}", got)
	}
}

func TestRepairNeverChangesFields(t *testing.T) {
	spec := mustSpec(t, RungJSONMode)
	for _, content := range []string{
		`{"answer": "seven",}`,
		`{}`,
		`{"other": 7,}`,
	} {
		if got, ok := spec.Repair(replyWith(t, content)); ok {
			t.Fatalf("Repair fabricated %s from %s", got, content)
		}
	}
}

func TestRepairKeepsCommasInsideStrings(t *testing.T) {
	spec, err := NewSpec(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`), []Rung{RungJSONMode})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := spec.Repair(replyWith(t, `{"answer": "one, two",}`))
	if !ok || string(got) != `{"answer":"one, two"}` {
		t.Fatalf("Repair = %s, %v", got, ok)
	}
}

func TestRepairFixesTypographicQuotes(t *testing.T) {
	spec := mustSpec(t, RungJSONMode)
	got, ok := spec.Repair(replyWith(t, "{\u201canswer\u201d: 7}"))
	if !ok || string(got) != `{"answer":7}` {
		t.Fatalf("Repair = %s, %v", got, ok)
	}
}

func TestRepairEscapesAnInvalidEscape(t *testing.T) {
	spec, err := NewSpec(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`), []Rung{RungJSONMode})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := spec.Repair(replyWith(t, `{"answer": "a\db"}`))
	if !ok {
		t.Fatal("Repair did not accept an invalid escape")
	}
	var value struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(got, &value); err != nil {
		t.Fatalf("Repair = %s: %v", got, err)
	}
	if value.Answer != `a\db` {
		t.Fatalf("Repair changed the value to %q", value.Answer)
	}
}

func TestClientRef(t *testing.T) {
	if got := ClientRef("call-1", 1); got != "call-1" {
		t.Fatalf("attempt 1 ref = %q, want call-1", got)
	}
	a := ClientRef("call-1", 2)
	b := ClientRef("call-1", 2)
	c := ClientRef("call-1", 3)
	if a != b {
		t.Fatalf("attempt 2 ref is not deterministic: %q vs %q", a, b)
	}
	if a == "call-1" || a == c {
		t.Fatalf("attempt refs collide: %q %q", a, c)
	}
	if got := ClientRef("call-2", 2); got == a {
		t.Fatalf("different calls share the attempt ref %q", got)
	}
}

func TestRequestForNativeAndJSONMode(t *testing.T) {
	base := model.Request{Model: "sim-chat", ClientRef: "c1", Params: json.RawMessage(`{"temperature":0.2}`)}
	for _, rung := range []Rung{RungNativeSchema, RungJSONMode} {
		req, err := RequestFor(base, rung, json.RawMessage(answerSchema))
		if err != nil {
			t.Fatalf("RequestFor(%s): %v", rung, err)
		}
		if string(req.OutputSchema) != answerSchema {
			t.Fatalf("%s OutputSchema = %s", rung, req.OutputSchema)
		}
		var params map[string]any
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params["temperature"] != 0.2 {
			t.Fatalf("%s dropped existing params: %s", rung, req.Params)
		}
		want := "json_object"
		if rung == RungNativeSchema {
			want = "json_schema"
		}
		if params["response_format"] != want {
			t.Fatalf("%s response_format = %v, want %s", rung, params["response_format"], want)
		}
	}
}

func TestRequestForForcedTool(t *testing.T) {
	req, err := RequestFor(model.Request{Model: "sim-chat"}, RungForcedTool, json.RawMessage(answerSchema))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.OutputSchema) != 0 {
		t.Fatalf("forced tool still carries a native schema: %s", req.OutputSchema)
	}
	var tools []map[string]any
	if err := json.Unmarshal(req.Tools, &tools); err != nil {
		t.Fatalf("tools = %s: %v", req.Tools, err)
	}
	if len(tools) != 1 || tools[0]["name"] != "structured_output" {
		t.Fatalf("tools = %s", req.Tools)
	}
}
