package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/itchyny/gojq"
)

// MappingOption configures a mapping.
type MappingOption func(*Mapping) error

// WithStepLimit sets the deterministic step limit (default 1_000_000).
func WithStepLimit(limit uint64) MappingOption {
	return func(m *Mapping) error {
		m.stepLimit = limit
		return nil
	}
}

// Mapping is a compiled gojq program.
type Mapping struct {
	src       string
	code      *gojq.Code
	vars      []string
	stepLimit uint64
}

// CompileMapping parses and validates a jq mapping.
func CompileMapping(src string, varNames []string, opts ...MappingOption) (*Mapping, error) {
	for _, v := range varNames {
		if v == "$ENV" {
			return nil, mappingCompileErr(src, `undeclared reference "$ENV"`)
		}
	}
	query, err := gojq.Parse(src)
	if err != nil {
		return nil, formatMappingParseErr(src, err)
	}
	if name, blocked := scanBanned(query); blocked {
		if name == "unsupported construct" {
			return nil, mappingCompileErr(src, name)
		}
		return nil, mappingCompileErr(src, fmt.Sprintf(`undeclared reference "%s"`, name))
	}
	code, err := gojq.Compile(query,
		append([]gojq.CompilerOption{
			gojq.WithVariables(varNames),
			gojq.WithEnvironLoader(func() []string { return nil }),
		}, blockedBuiltinCompilerOptions()...)...,
	)
	if err != nil {
		return nil, mappingCompileErr(src, err.Error())
	}
	m := &Mapping{
		src:       src,
		code:      code,
		vars:      append([]string(nil), varNames...),
		stepLimit: defaultStepLimit,
	}
	for _, o := range opts {
		if err := o(m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Eval runs the mapping on input with named variables bound in declaration order.
func (m *Mapping) Eval(input any, vars map[string]any) (any, error) {
	normVars, err := normalizeMappingVars(vars)
	if err != nil {
		return nil, mappingEvalErr("mapping evaluation failed")
	}
	values := make([]any, len(m.vars))
	for i, name := range m.vars {
		values[i] = normVars[name]
	}
	normIn, err := normalizeMappingInput(input)
	if err != nil {
		return nil, mappingEvalErr("mapping evaluation failed")
	}
	input = normIn
	ctx := newStepContext(m.stepLimit)
	iter := m.code.RunWithContext(ctx, input, values...)
	var first any
	var count int
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			if ctx.limitExceeded() {
				return nil, mappingEvalErr(fmt.Sprintf("step limit %d exceeded", m.stepLimit))
			}
			cause := mapGoJQCause(err.Error())
			if cause == "" {
				cause = "mapping evaluation failed"
			}
			return nil, mappingEvalErr(cause)
		}
		count++
		if count == 1 {
			first, err = normalizeMappingOutput(v)
			if err != nil {
				return nil, mappingEvalErr("mapping evaluation failed")
			}
			continue
		}
		return nil, mappingEvalErr("mapping produced more than one value")
	}
	if count == 0 {
		return nil, mappingEvalErr("mapping produced no value")
	}
	if count > 1 {
		return nil, mappingEvalErr("mapping produced more than one value")
	}
	return first, nil
}

func formatMappingParseErr(src string, err error) error {
	var pe *gojq.ParseError
	if errors.As(err, &pe) {
		return mappingCompileErr(src, fmt.Sprintf("parse error at offset %d", pe.Offset))
	}
	return mappingCompileErr(src, err.Error())
}

func normalizeMappingInput(v any) (any, error) {
	w := newJSONWalk()
	return normalizeMappingInputWalk(v, w)
}

func normalizeMappingInputWalk(v any, w *jsonWalk) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		return normalizeMappingInputMap(t, w)
	case nil:
		return nil, nil
	default:
		return normalizeMappingInputValue(v, w)
	}
}

func normalizeMappingInputMap(m map[string]any, w *jsonWalk) (map[string]any, error) {
	ptr := addrOf(m)
	if err := w.down(ptr); err != nil {
		return nil, err
	}
	defer w.up(ptr)
	out := make(map[string]any, len(m))
	for k, val := range m {
		nv, err := normalizeMappingInputValue(val, w)
		if err != nil {
			return nil, err
		}
		out[k] = nv
	}
	return out, nil
}

func mapGoJQCause(msg string) string {
	switch {
	case strings.Contains(msg, "cannot be sorted"):
		return "mapping produced more than one value"
	case strings.Contains(msg, "division by zero"):
		return "division by zero"
	case strings.Contains(strings.ToLower(msg), "overflow"):
		return "overflow"
	case strings.Contains(msg, "index out of range"), strings.Contains(msg, "out of bounds"):
		return "out of bounds"
	case strings.Contains(msg, "no such key"), strings.Contains(msg, "does not exist"):
		return "no such key"
	default:
		return ""
	}
}

func normalizeMappingInputValue(v any, w *jsonWalk) (any, error) {
	switch t := v.(type) {
	case int64:
		return int(t), nil
	case int:
		return t, nil
	case json.Number:
		bi := new(big.Int)
		if _, ok := bi.SetString(string(t), 10); ok {
			if bi.IsInt64() {
				return int(bi.Int64()), nil
			}
			return bi, nil
		}
		if f, err := t.Float64(); err == nil {
			return f, nil
		}
		return t, nil
	case map[string]any:
		return normalizeMappingInputMap(t, w)
	case []any:
		ptr := addrOf(t)
		if err := w.down(ptr); err != nil {
			return nil, err
		}
		defer w.up(ptr)
		out := make([]any, len(t))
		for i, e := range t {
			nv, err := normalizeMappingInputValue(e, w)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	default:
		return v, nil
	}
}

func normalizeMappingOutput(v any) (any, error) {
	w := newJSONWalk()
	return normalizeMappingOutputWalk(v, w)
}

func normalizeMappingOutputWalk(v any, w *jsonWalk) (any, error) {
	switch t := v.(type) {
	case *big.Int:
		if t.IsInt64() {
			return t.Int64(), nil
		}
		return json.Number(t.String()), nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		if f, err := t.Float64(); err == nil {
			return f, nil
		}
		return t, nil
	case map[string]any:
		ptr := addrOf(t)
		if err := w.down(ptr); err != nil {
			return nil, err
		}
		defer w.up(ptr)
		for k, val := range t {
			nv, err := normalizeMappingOutputWalk(val, w)
			if err != nil {
				return nil, err
			}
			t[k] = nv
		}
		return t, nil
	case []any:
		ptr := addrOf(t)
		if err := w.down(ptr); err != nil {
			return nil, err
		}
		defer w.up(ptr)
		out := make([]any, len(t))
		for i, e := range t {
			nv, err := normalizeMappingOutputWalk(e, w)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	default:
		return v, nil
	}
}

func scanBanned(query *gojq.Query) (string, bool) {
	return scanBannedAST(query)
}

var bannedNames = map[string]bool{
	"now": true, "localtime": true, "strflocaltime": true,
	"env": true, "$ENV": true,
	"input": true, "inputs": true, "input_filename": true, "input_line_number": true,
	"debug": true, "stderr": true, "halt": true, "halt_error": true,
	"import": true, "include": true, "modulemeta": true, "get_search_list": true,
}

// blockedBuiltinCompilerOptions registers impure builtins to return errors at runtime
// if static analysis misses a call (second layer; compile-time scan remains primary).
func blockedBuiltinCompilerOptions() []gojq.CompilerOption {
	names := []string{
		"now", "localtime", "strflocaltime", "env",
		"input", "inputs", "input_filename", "input_line_number",
		"debug", "stderr", "halt", "halt_error", "get_search_list",
	}
	opts := make([]gojq.CompilerOption, 0, len(names))
	block := func(_ any, _ []any) any {
		return fmt.Errorf("undeclared reference")
	}
	for _, n := range names {
		name := n
		opts = append(opts, gojq.WithFunction(name, 0, 30, block))
	}
	return opts
}

func normalizeMappingVars(vars map[string]any) (map[string]any, error) {
	if len(vars) == 0 {
		return vars, nil
	}
	out := make(map[string]any, len(vars))
	for _, name := range sortedVarNames(vars) {
		nv, err := normalizeMappingInput(vars[name])
		if err != nil {
			return nil, err
		}
		out[name] = nv
	}
	return out, nil
}
