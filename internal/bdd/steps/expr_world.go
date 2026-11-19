package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
)

type exprWorld struct {
	varDecls          []expr.Var
	costLimit         *uint64
	stepLimit         *uint64
	outputLimit       *int
	env               *expr.Env
	envErr            error
	program           *expr.Program
	compileErr        error
	resultType        string
	mapping           *expr.Mapping
	mapCompile        error
	bindings          map[string]any
	lastErr           error
	result            any
	results           []any
	evalResults       [2]any
	evalTimes         int
	expectedEvalCount int
	lastExpr          string
}

func exprDeclare(ctx context.Context, name, schema string) error {
	e := world(ctx).Expr
	raw := json.RawMessage(schema)
	for i, v := range e.varDecls {
		if v.Name == name {
			e.varDecls[i].Schema = raw
			return exprRebuildEnv(ctx)
		}
	}
	e.varDecls = append(e.varDecls, expr.Var{Name: name, Schema: raw})
	return exprRebuildEnv(ctx)
}

func exprDeclareCost(ctx context.Context, name, schema string, limit uint64) error {
	e := world(ctx).Expr
	e.costLimit = &limit
	return exprDeclare(ctx, name, schema)
}

func exprNewEnv(ctx context.Context, vars []expr.Var, limit *uint64) error {
	e := world(ctx).Expr
	e.varDecls = vars
	e.costLimit = limit
	e.envErr = nil
	e.env = nil
	e.bindings = map[string]any{}
	return exprRebuildEnv(ctx)
}

func exprRebuildEnv(ctx context.Context) error {
	e := world(ctx).Expr
	var opts []expr.EnvOption
	if e.costLimit != nil {
		opts = append(opts, expr.WithCostLimit(*e.costLimit))
	}
	env, err := expr.NewEnv(e.varDecls, opts...)
	if err != nil {
		e.envErr = err
		e.env = nil
		return nil
	}
	e.env = env
	e.envErr = nil
	return nil
}

func exprCompileImpl(ctx context.Context, src, resultSchema string) error {
	e := world(ctx).Expr
	e.lastExpr = src
	e.program = nil
	e.compileErr = nil
	e.resultType = ""
	if e.env == nil {
		if e.envErr != nil {
			e.compileErr = e.envErr
			return nil
		}
		if err := exprNewEnv(ctx, e.varDecls, e.costLimit); err != nil {
			e.compileErr = err
			return nil
		}
	}
	var result json.RawMessage
	if resultSchema != "" {
		result = json.RawMessage(resultSchema)
	}
	p, err := e.env.Compile(src, result)
	if err != nil {
		e.compileErr = err
		return nil
	}
	e.program = p
	e.resultType = p.ResultType()
	return nil
}

func exprEval(ctx context.Context, src string, n int, _ string, _ map[string]any) error {
	e := world(ctx).Expr
	e.lastExpr = src
	if err := exprCompileImpl(ctx, src, ""); err != nil {
		return err
	}
	if e.compileErr != nil {
		e.lastErr = e.compileErr
		return nil
	}
	e.expectedEvalCount = n
	e.results = e.results[:0]
	for i := 0; i < n; i++ {
		res, err := e.program.Eval(copyBindings(e.bindings))
		if err != nil {
			e.lastErr = err
			e.results = e.results[:0]
			return nil
		}
		e.results = append(e.results, res)
	}
	if n == 1 {
		e.result = e.results[0]
		e.evalResults[0] = e.results[0]
	}
	e.lastErr = nil
	return nil
}

func exprEvalAgain(ctx context.Context, src string) error {
	e := world(ctx).Expr
	if err := exprCompileImpl(ctx, src, ""); err != nil {
		return err
	}
	if e.compileErr != nil {
		e.lastErr = e.compileErr
		return nil
	}
	res, err := e.program.Eval(copyBindings(e.bindings))
	if err != nil {
		e.lastErr = err
		return nil
	}
	if e.evalResults[0] == nil {
		e.evalResults[0] = e.result
	}
	e.evalResults[1] = res
	e.evalTimes = 2
	e.result = res
	e.lastErr = nil
	return nil
}

func copyBindings(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func exprMappingDeclare(ctx context.Context, src string, vars []string, limit *uint64) error {
	e := world(ctx).Expr
	var opts []expr.MappingOption
	if limit != nil {
		opts = append(opts, expr.WithStepLimit(*limit))
	} else if e.stepLimit != nil {
		opts = append(opts, expr.WithStepLimit(*e.stepLimit))
	}
	if e.outputLimit != nil {
		opts = append(opts, expr.WithOutputLimit(*e.outputLimit))
	}
	m, err := expr.CompileMapping(src, vars, opts...)
	if err != nil {
		e.mapCompile = err
		e.mapping = nil
		return nil
	}
	e.mapping = m
	e.mapCompile = nil
	return nil
}

func exprMappingDeclareOutput(ctx context.Context, src string, limit int) error {
	e := world(ctx).Expr
	e.outputLimit = &limit
	return exprMappingDeclare(ctx, src, nil, nil)
}

func exprMappingEval(ctx context.Context, input any, n int) error {
	e := world(ctx).Expr
	if e.mapping == nil {
		return fmt.Errorf("no compiled mapping")
	}
	e.expectedEvalCount = n
	e.results = e.results[:0]
	for i := 0; i < n; i++ {
		res, err := e.mapping.Eval(input, copyBindings(e.bindings))
		if err != nil {
			e.lastErr = err
			e.results = e.results[:0]
			return nil
		}
		e.results = append(e.results, res)
	}
	if n == 1 {
		e.result = e.results[0]
	}
	e.lastErr = nil
	return nil
}

func exprMsgContains(ctx context.Context, parts ...string) error {
	e := world(ctx).Expr
	err := e.lastErr
	if err == nil {
		err = e.compileErr
	}
	if err == nil {
		err = e.mapCompile
	}
	if err == nil {
		err = e.envErr
	}
	return exprMsgContainsErr(err, e.lastExpr, parts...)
}

func exprMsgContainsErr(err error, exprSrc string, parts ...string) error {
	if err == nil {
		return fmt.Errorf("no error, want message containing %v", parts)
	}
	msg := errMsg(err)
	for _, p := range parts {
		if p != "" && !strings.Contains(msg, p) {
			return fmt.Errorf("message %q missing %q", msg, p)
		}
	}
	if exprSrc != "" && !strings.Contains(msg, exprSrc) {
		return fmt.Errorf("message %q missing expression %q", msg, exprSrc)
	}
	return nil
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe.Message
	}
	return err.Error()
}

var intRangeRe = regexp.MustCompile(`^the integers (\d+) to (\d+)$`)
var costLimitSuffixRe = regexp.MustCompile(`^(.+) and a cost limit of (\d+)$`)

func parseJSONValue(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if m := intRangeRe.FindStringSubmatch(raw); len(m) == 3 {
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		out := make([]any, 0, hi-lo+1)
		for i := lo; i <= hi; i++ {
			out = append(out, int64(i))
		}
		return out, nil
	}
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, err
		}
		return s, nil
	}
	if isDigitString(raw) {
		return json.Number(raw), nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n, nil
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("parse JSON value %q: %w", raw, err)
	}
	return normalizeParsedJSON(v), nil
}

func normalizeParsedJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeParsedJSON(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeParsedJSON(val)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		bi := new(big.Int)
		if _, ok := bi.SetString(string(t), 10); ok {
			if bi.IsInt64() {
				return bi.Int64()
			}
			return t
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t
	default:
		return v
	}
}

func jsonValuesEqual(got, want any) error {
	gj, err := json.Marshal(canonicalJSON(got))
	if err != nil {
		return err
	}
	wj, err := json.Marshal(canonicalJSON(want))
	if err != nil {
		return err
	}
	if string(gj) != string(wj) {
		return fmt.Errorf("got %s, want %s", gj, wj)
	}
	return nil
}

func canonicalJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = canonicalJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = canonicalJSON(val)
		}
		return out
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case float32:
		return float64(t)
	default:
		return v
	}
}

func isDigitString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
