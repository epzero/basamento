package providers

import (
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
	"github.com/epzero/basamento/model"
)

// compileMapping compiles one gojq expression. Manifests are data; a bad
// expression is a configuration error, reported when the manifest loads or the
// provider is constructed, never at invocation time.
func compileMapping(src string) (*expr.Mapping, error) {
	return expr.CompileMapping(src, nil)
}

// evalMapping runs a mapping over input and returns its single value.
func evalMapping(m *expr.Mapping, input any) (any, error) {
	v, err := m.Eval(input, nil)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "provider manifest mapping failed", nil)
	}
	return v, nil
}

// decodeJSON decodes a raw JSON value into the generic form gojq expects. An
// empty value decodes as nil.
func decodeJSON(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "provider request has invalid JSON", nil)
	}
	return v, nil
}

// jsonValue converts a Go value to its generic JSON form, so gojq sees the
// same shapes a manifest author sees in a JSON document.
func jsonValue(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "provider cannot encode a value", nil)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, core.NewError(core.CodeInternal, "provider cannot decode its own encoding", nil)
	}
	return out, nil
}

// invocationInput is the document every manifest mapping sees: the request,
// the vendor id once known, and the working directory for a command.
type invocationInput struct {
	Model     string            `json:"model"`
	Op        string            `json:"op"`
	ClientRef string            `json:"client_ref"`
	ID        string            `json:"id"`
	WorkDir   string            `json:"workdir,omitempty"`
	BaseURL   string            `json:"base_url,omitempty"`
	Params    any               `json:"params,omitempty"`
	Messages  any               `json:"messages,omitempty"`
	Tools     any               `json:"tools,omitempty"`
	Schema    any               `json:"output_schema,omitempty"`
	Text      map[string]string `json:"text,omitempty"`
	Texts     []string          `json:"texts,omitempty"`
	Assets    any               `json:"assets,omitempty"`
	Seed      *int64            `json:"seed,omitempty"`
}

func inputFor(r model.Request, id, workdir string) (map[string]any, error) {
	params, err := decodeJSON(r.Params)
	if err != nil {
		return nil, err
	}
	messages, err := decodeJSON(r.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := decodeJSON(r.Tools)
	if err != nil {
		return nil, err
	}
	schema, err := decodeJSON(r.OutputSchema)
	if err != nil {
		return nil, err
	}
	assets, err := jsonValue(r.Assets)
	if err != nil {
		return nil, err
	}
	in := invocationInput{
		Model:     r.Model,
		Op:        string(r.Op),
		ClientRef: r.ClientRef,
		ID:        id,
		WorkDir:   workdir,
		Params:    params,
		Messages:  messages,
		Tools:     tools,
		Schema:    schema,
		Text:      r.Text,
		Texts:     r.Texts,
		Assets:    assets,
		Seed:      r.Seed,
	}
	v, err := jsonValue(in)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, core.NewError(core.CodeInternal, "provider invocation input is not an object", nil)
	}
	return obj, nil
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return fmt.Sprintf("%v", x)
	case bool:
		return fmt.Sprintf("%v", x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
