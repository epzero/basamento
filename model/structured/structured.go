package structured

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/epzero/basamento/core"
	coreschema "github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/model"
)

// Rung is one step of the structured-output ladder.
type Rung string

const (
	// RungNone marks a chat call that did not ask for structured output.
	RungNone Rung = "none"
	// RungNativeSchema asks the vendor for its native JSON-schema mode.
	RungNativeSchema Rung = "native_schema"
	// RungJSONMode asks the vendor for a JSON object without a schema.
	RungJSONMode Rung = "json_mode"
	// RungForcedTool asks the vendor to answer through one forced tool call.
	RungForcedTool Rung = "forced_tool"
	// RungLocalRepair repairs the previous answer in process, with no model
	// invocation (REQ-MODEL-065).
	RungLocalRepair Rung = "local_repair"
)

// ladder is the order the rungs are tried in (REQ-MODEL-044).
var ladder = []Rung{RungNativeSchema, RungJSONMode, RungForcedTool}

// Ladder returns the rungs in the order a call tries them.
func Ladder() []Rung { return append([]Rung(nil), ladder...) }

// Display returns the rung as the specs spell it.
func (r Rung) Display() string {
	switch r {
	case RungNativeSchema:
		return "native schema"
	case RungJSONMode:
		return "JSON mode"
	case RungForcedTool:
		return "forced tool call"
	case RungLocalRepair:
		return "local repair"
	default:
		return string(r)
	}
}

// ParseRung reads a rung from its spec spelling or its stored value.
func ParseRung(s string) (Rung, error) {
	for _, r := range append(ladder, RungNone, RungLocalRepair) {
		if s == r.Display() || s == string(r) {
			return r, nil
		}
	}
	return "", core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("unknown structured-output rung %q", s), nil)
}

// ParseRungs reads a comma-separated rung list, as the specs spell it. The
// empty list is a model that supports no rung.
func ParseRungs(s string) ([]Rung, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]Rung, 0, len(parts))
	for _, part := range parts {
		r, err := ParseRung(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		if r == RungNone || r == RungLocalRepair {
			return nil, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("%q is not a model rung", part), nil)
		}
		out = append(out, r)
	}
	return out, nil
}

// Spec is a structured-output call: one output schema and the rungs the model
// supports, in ladder order.
type Spec struct {
	raw       json.RawMessage
	compiled  *coreschema.Compiled
	supported []Rung
}

// NewSpec compiles an output schema and keeps the supported rungs in ladder
// order. It fails with "invalid_argument" when the schema does not compile or
// a rung is unknown.
func NewSpec(raw json.RawMessage, supported []Rung) (*Spec, error) {
	if len(raw) == 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "structured output has no schema", nil)
	}
	compiled, err := coreschema.Compile(raw)
	if err != nil {
		return nil, err
	}
	seen := map[Rung]bool{}
	for _, r := range supported {
		switch r {
		case RungNativeSchema, RungJSONMode, RungForcedTool:
			seen[r] = true
		case RungNone, RungLocalRepair:
			return nil, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("%q is not a model rung", r), nil)
		default:
			return nil, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("unknown structured-output rung %q", r), nil)
		}
	}
	ordered := make([]Rung, 0, len(seen))
	for _, r := range ladder {
		if seen[r] {
			ordered = append(ordered, r)
		}
	}
	return &Spec{raw: append(json.RawMessage(nil), raw...), compiled: compiled, supported: ordered}, nil
}

// Schema returns the output schema.
func (s *Spec) Schema() json.RawMessage { return append(json.RawMessage(nil), s.raw...) }

// Supported returns the model's rungs in ladder order.
func (s *Spec) Supported() []Rung { return append([]Rung(nil), s.supported...) }

// Validate extracts the answer from a chat reply and checks it against the
// output schema. It returns the canonical JSON of the answer when it holds.
func (s *Spec) Validate(message json.RawMessage) (json.RawMessage, bool) {
	answer, ok := answerBytes(message)
	if !ok {
		return nil, false
	}
	var out bytes.Buffer
	if err := json.Compact(&out, answer); err != nil {
		return nil, false
	}
	if err := s.compiled.Validate(out.Bytes()); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// Repair strips prose and code fences around the answer, fixes JSON syntax and
// validates the result. It never adds, removes or changes a field, so an
// answer that merely lacks one still does not satisfy the schema.
func (s *Spec) Repair(message json.RawMessage) (json.RawMessage, bool) {
	answer, ok := answerBytes(message)
	if !ok {
		return nil, false
	}
	repaired, ok := repairJSON(string(answer))
	if !ok {
		return nil, false
	}
	if err := s.compiled.Validate(repaired); err != nil {
		return nil, false
	}
	return repaired, true
}

// RequestFor builds the request of one rung. Native schema and JSON mode carry
// the schema through OutputSchema and a vendor response-format parameter;
// forced tool call carries it as a tool definition (design.md §2).
func RequestFor(base model.Request, rung Rung, spec json.RawMessage) (model.Request, error) {
	switch rung {
	case RungNativeSchema, RungJSONMode:
		format := "json_object"
		if rung == RungNativeSchema {
			format = "json_schema"
		}
		params, err := withResponseFormat(base.Params, format)
		if err != nil {
			return model.Request{}, err
		}
		base.OutputSchema = append(json.RawMessage(nil), spec...)
		base.Params = params
		return base, nil
	case RungForcedTool:
		tools, err := forcedTool(spec)
		if err != nil {
			return model.Request{}, err
		}
		base.OutputSchema = nil
		base.Tools = tools
		return base, nil
	default:
		return model.Request{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("%q is not a model rung", rung), nil)
	}
}

// ClientRef derives the deterministic client ref of attempt n of one logical
// call (REQ-MODEL-054). Attempt 1 keeps the call's ref; later attempts get a
// stable hash, never a literal the call's ref can collide with.
func ClientRef(callRef string, attempt int) string {
	if attempt <= 1 {
		return callRef
	}
	sum := sha256.Sum256([]byte(callRef + "\x00" + fmt.Sprint(attempt)))
	return "ref-" + hex.EncodeToString(sum[:16])
}

func withResponseFormat(params json.RawMessage, format string) (json.RawMessage, error) {
	merged := map[string]any{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &merged); err != nil {
			return nil, core.NewError(core.CodeInvalidArgument,
				"params are not a JSON object", map[string]any{"cause": err.Error()})
		}
	}
	merged["response_format"] = format
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "cannot encode structured-output params", nil)
	}
	return out, nil
}

func forcedTool(spec json.RawMessage) (json.RawMessage, error) {
	tool := map[string]any{
		"name":              "structured_output",
		"desc":              "Answer with the call's output schema",
		"has_params_one_of": true,
		// eino's ToolInfo carries a JSON schema as an encoded string.
		"json_schema": string(spec),
	}
	out, err := json.Marshal([]any{tool})
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "cannot encode the forced tool", nil)
	}
	return out, nil
}

// reply mirrors the parts of an eino chat message structured output reads.
type reply struct {
	Content   string `json:"content"`
	ToolCalls []struct {
		Function struct {
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// answerBytes returns the answer a reply carries: a forced tool call's
// arguments when present, otherwise the message text.
func answerBytes(message json.RawMessage) (json.RawMessage, bool) {
	if len(message) == 0 {
		return nil, false
	}
	var r reply
	if err := json.Unmarshal(message, &r); err != nil {
		return nil, false
	}
	for _, call := range r.ToolCalls {
		if strings.TrimSpace(call.Function.Arguments) != "" {
			return []byte(call.Function.Arguments), true
		}
	}
	if strings.TrimSpace(r.Content) == "" {
		return nil, false
	}
	return []byte(r.Content), true
}

// repairJSON strips the prose and fences around a JSON value and fixes its
// syntax. The repairs are textual: a trailing comma or a fence disappears, no
// member is ever added, dropped or rewritten.
func repairJSON(s string) ([]byte, bool) {
	s = stripFences(strings.TrimSpace(s))
	s = outerValue(s)
	if s == "" {
		return nil, false
	}
	s = removeTrailingCommas(s)
	if !json.Valid([]byte(s)) {
		return nil, false
	}
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(s)); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

func stripFences(s string) string {
	start := strings.Index(s, "```")
	if start < 0 {
		return s
	}
	rest := s[start+3:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		return s
	}
	if end := strings.Index(rest, "```"); end >= 0 {
		return rest[:end]
	}
	return s
}

// outerValue returns the substring from the first JSON object or array to its
// matching close, ignoring brackets inside strings.
func outerValue(s string) string {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '{' || s[i] == '[' {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}
	depth := 0
	inString, escaped := false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

func removeTrailingCommas(s string) string {
	out := make([]byte, 0, len(s))
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return string(out)
}
