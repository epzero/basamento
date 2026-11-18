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

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
)

func registerExpr(sc *godog.ScenarioContext) {
	sc.Step(`^an expression environment declaring no variables$`, func(ctx context.Context) error {
		return exprNewEnv(ctx, nil, nil)
	})

	sc.Step(`^an expression environment declaring "([^"]*)" with schema:$`, func(ctx context.Context, name string, doc *godog.DocString) error {
		return exprDeclare(ctx, name, strings.TrimSpace(doc.Content))
	})

	sc.Step(`^an expression environment declaring "([^"]*)" with schema (.+)$`, func(ctx context.Context, name, tail string) error {
		tail = strings.TrimSpace(tail)
		if m := costLimitSuffixRe.FindStringSubmatch(tail); len(m) == 3 {
			limit, err := strconv.ParseUint(m[2], 10, 64)
			if err != nil {
				return err
			}
			return exprDeclareCost(ctx, name, strings.TrimSpace(m[1]), limit)
		}
		return exprDeclare(ctx, name, tail)
	})

	sc.Step(`^an expression environment declaring "([^"]*)" as an integer, "([^"]*)" as a list of integers and "([^"]*)" as a map of integers$`,
		func(ctx context.Context, n, items, m string) error {
			vars := []expr.Var{
				{Name: n, Schema: json.RawMessage(`{"type":"integer"}`)},
				{Name: items, Schema: json.RawMessage(`{"type":"array","items":{"type":"integer"}}`)},
				{Name: m, Schema: json.RawMessage(`{"type":"object","additionalProperties":{"type":"integer"}}`)},
			}
			return exprNewEnv(ctx, vars, nil)
		})

	sc.Step(`^an expression environment declaring "([^"]*)" with an optional string property "([^"]*)"$`,
		func(ctx context.Context, state, prop string) error {
			schema := fmt.Sprintf(`{"type":"object","properties":{"%s":{"type":"string"}}}`, prop)
			return exprDeclare(ctx, state, schema)
		})

	sc.Step(`^the expression "([^"]*)" is compiled$`, func(ctx context.Context, src string) error {
		return exprCompileImpl(ctx, src, "")
	})
	sc.Step(`^the expression "([^"]*)" is compiled expecting a result of schema (.+)$`,
		func(ctx context.Context, src, resultSchema string) error {
			return exprCompileImpl(ctx, src, strings.TrimSpace(resultSchema))
		})

	sc.Step(`^compilation succeeds with result type "([^"]*)"$`, func(ctx context.Context, want string) error {
		e := world(ctx).Expr
		if e.compileErr != nil {
			return fmt.Errorf("compilation failed: %v", e.compileErr)
		}
		if e.program == nil {
			return fmt.Errorf("no compiled program")
		}
		got := e.resultType
		if got != want {
			return fmt.Errorf("result type %q, want %q", got, want)
		}
		return nil
	})

	sc.Step(`^compilation fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		e := world(ctx).Expr
		var err error
		switch {
		case e.mapCompile != nil:
			err = e.mapCompile
		case e.compileErr != nil:
			err = e.compileErr
		case e.envErr != nil:
			err = e.envErr
		default:
			return fmt.Errorf("compilation succeeded, want %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %v", got, code, err)
		}
		return nil
	})

	sc.Step(`^the error names the expression and the line and column of the error$`, func(ctx context.Context) error {
		return exprMsgContains(ctx, "line", "column")
	})

	sc.Step(`^the error names the undeclared reference "([^"]*)"$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name)
	})

	sc.Step(`^the error names the undeclared property "([^"]*)"$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name)
	})

	sc.Step(`^the error names the expected type "([^"]*)" and the actual type "([^"]*)"$`,
		func(ctx context.Context, exp, act string) error {
			return exprMsgContains(ctx, exp, act)
		})

	sc.Step(`^the error names the reference "([^"]*)"$`, func(ctx context.Context, ref string) error {
		return exprMsgContains(ctx, ref)
	})

	sc.Step(`^the expression "([^"]*)" is evaluated$`, func(ctx context.Context, src string) error {
		return exprEval(ctx, src, 1, "", nil)
	})

	sc.Step(`^the expression "([^"]*)" is evaluated (\d+) times$`, func(ctx context.Context, src string, n int) error {
		return exprEval(ctx, src, n, "", nil)
	})

	sc.Step(`^the expression "([^"]*)" is evaluated with "([^"]*)" bound to (.+)$`,
		func(ctx context.Context, src, varName, raw string) error {
			val, err := parseJSONValue(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[varName] = val
			return exprEval(ctx, src, 1, "", nil)
		})

	sc.Step(`^the expression "([^"]*)" is evaluated (\d+) times with "([^"]*)" bound to (.+)$`,
		func(ctx context.Context, src string, n int, varName, raw string) error {
			val, err := parseJSONValue(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[varName] = val
			return exprEval(ctx, src, n, "", nil)
		})

	sc.Step(`^the expression "([^"]*)" is evaluated again with "([^"]*)" bound to (.+)$`,
		func(ctx context.Context, src, varName, raw string) error {
			val, err := parseJSONValue(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[varName] = val
			return exprEvalAgain(ctx, src)
		})

	sc.Step(`^"([^"]*)" is bound to (.+), "([^"]*)" to (.+) and "([^"]*)" to (.+)$`,
		func(ctx context.Context, n1, v1, n2, v2, n3, v3 string) error {
			e := world(ctx).Expr
			for _, pair := range []struct {
				name, raw string
			}{{n1, v1}, {n2, v2}, {n3, v3}} {
				val, err := parseJSONValue(strings.TrimSpace(pair.raw))
				if err != nil {
					return err
				}
				e.bindings[pair.name] = val
			}
			return nil
		})

	sc.Step(`^evaluation fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := world(ctx).Expr.lastErr
		if err == nil {
			return fmt.Errorf("evaluation succeeded, want %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %v", got, code, err)
		}
		return nil
	})

	sc.Step(`^the error names the expression and the cost limit (\d+)$`, func(ctx context.Context, limit int) error {
		e := world(ctx).Expr
		return exprMsgContainsErr(e.lastErr, e.lastExpr, strconv.Itoa(limit), "cost limit")
	})

	sc.Step(`^the error names the expression and the cause "([^"]*)"$`, func(ctx context.Context, cause string) error {
		e := world(ctx).Expr
		return exprMsgContainsErr(e.lastErr, e.lastExpr, cause)
	})

	sc.Step(`^the error names the variable "([^"]*)"$`, func(ctx context.Context, name string) error {
		err := world(ctx).Expr.lastErr
		if err == nil {
			return fmt.Errorf("no error, want variable %q", name)
		}
		msg := errMsg(err)
		if !strings.Contains(msg, name) {
			return fmt.Errorf("message %q missing variable %q", msg, name)
		}
		return nil
	})

	sc.Step(`^the result is (.+)$`, func(ctx context.Context, raw string) error {
		want, err := parseJSONValue(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		return jsonValuesEqual(world(ctx).Expr.result, want)
	})

	sc.Step(`^both results are (\d+)$`, func(ctx context.Context, n int) error {
		e := world(ctx).Expr
		if e.evalResults[0] == nil {
			e.evalResults[0] = e.result
		}
		for i, r := range e.evalResults[:] {
			if r == nil {
				return fmt.Errorf("missing eval result %d", i)
			}
			f, ok := asNumber(r)
			if !ok || int(f) != n {
				return fmt.Errorf("result %d got %v, want %d", i, r, n)
			}
		}
		return nil
	})

	sc.Step(`^every result is (.+)$`, func(ctx context.Context, raw string) error {
		want, err := parseJSONValue(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		for i, r := range world(ctx).Expr.results {
			if err := jsonValuesEqual(r, want); err != nil {
				return fmt.Errorf("result %d: %w", i, err)
			}
		}
		return nil
	})

	// Mappings
	sc.Step(`^the mapping "(.+?)"(?: with variable "([^"]*)")?$`, func(ctx context.Context, src, varName string) error {
		var vars []string
		if varName != "" {
			vars = []string{varName}
		}
		return exprMappingDeclare(ctx, src, vars, nil)
	})

	sc.Step(`^the mapping "(.+)" with a step limit of (\d+)$`, func(ctx context.Context, src string, limit uint64) error {
		return exprMappingDeclare(ctx, src, nil, &limit)
	})

	sc.Step(`^the mapping "(.+)" is compiled$`, func(ctx context.Context, src string) error {
		e := world(ctx).Expr
		_, err := expr.CompileMapping(src, nil)
		e.mapCompile = err
		if err != nil {
			e.lastErr = err
		}
		return nil
	})

	sc.Step(`^the mapping is evaluated on (.+)$`, func(ctx context.Context, rest string) error {
		rest = strings.TrimSpace(rest)
		var raw, vname, vraw string
		if i := strings.Index(rest, ` with "`); i >= 0 {
			suffix := rest[i+len(` with "`):]
			j := strings.Index(suffix, `" bound to `)
			if j >= 0 {
				raw = strings.TrimSpace(rest[:i])
				vname = suffix[:j]
				vraw = strings.TrimSpace(suffix[j+len(`" bound to `):])
			}
		}
		if raw == "" {
			raw = rest
		}
		in, err := parseJSONValue(raw)
		if err != nil {
			return err
		}
		if vname != "" {
			vval, err := parseJSONValue(vraw)
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[vname] = vval
		}
		return exprMappingEval(ctx, in, 1)
	})

	sc.Step(`^the mapping is evaluated (\d+) times on (.+)$`, func(ctx context.Context, n int, raw string) error {
		in, err := parseJSONValue(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		return exprMappingEval(ctx, in, n)
	})

	sc.Step(`^the error states that the mapping produced (.+)$`, func(ctx context.Context, phrase string) error {
		msg := strings.ToLower(errMsg(world(ctx).Expr.lastErr))
		switch strings.TrimSpace(phrase) {
		case "no value":
			if !strings.Contains(msg, "no value") {
				return fmt.Errorf("message %q does not say no value", msg)
			}
		case "more than one value":
			if !strings.Contains(msg, "more than one value") {
				return fmt.Errorf("message %q does not say more than one value", msg)
			}
		default:
			return fmt.Errorf("unknown count phrase %q", phrase)
		}
		return nil
	})

	sc.Step(`^the error names "([^"]*)"$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name)
	})

	sc.Step(`^the error names the step limit (\d+)$`, func(ctx context.Context, limit int) error {
		return exprMsgContains(ctx, strconv.Itoa(limit))
	})

	sc.Step(`^the error names the mapping and the position of the error$`, func(ctx context.Context) error {
		e := world(ctx).Expr
		if e.mapCompile == nil {
			return fmt.Errorf("no mapping compile error")
		}
		msg := errMsg(e.mapCompile)
		if !strings.Contains(msg, "mapping") {
			return fmt.Errorf("message does not name mapping: %s", msg)
		}
		if !strings.Contains(msg, "offset") && !strings.Contains(msg, "position") {
			return fmt.Errorf("message does not name position: %s", msg)
		}
		return nil
	})

}

type exprWorld struct {
	varDecls    []expr.Var
	costLimit   *uint64
	stepLimit   *uint64
	env         *expr.Env
	envErr      error
	program     *expr.Program
	compileErr  error
	resultType  string
	mapping     *expr.Mapping
	mapCompile  error
	bindings    map[string]any
	lastErr     error
	result      any
	results     []any
	evalResults [2]any
	evalTimes   int
	lastExpr    string
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
	e.results = e.results[:0]
	for i := 0; i < n; i++ {
		res, err := e.program.Eval(copyBindings(e.bindings))
		if err != nil {
			e.lastErr = err
			if i == 0 && n == 1 {
				return nil
			}
			return nil
		}
		e.results = append(e.results, res)
	}
	if n == 1 {
		e.result = e.results[0]
		e.evalResults[0] = e.results[0]
		e.lastErr = nil
	} else {
		e.lastErr = nil
	}
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

func exprMappingEval(ctx context.Context, input any, n int) error {
	e := world(ctx).Expr
	if e.mapping == nil {
		return fmt.Errorf("no compiled mapping")
	}
	e.results = e.results[:0]
	for i := 0; i < n; i++ {
		res, err := e.mapping.Eval(input, copyBindings(e.bindings))
		if err != nil {
			e.lastErr = err
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
			return json.Number(t)
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
