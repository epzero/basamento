package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
	"github.com/epzero/basamento/model"
)

var million = big.NewInt(1_000_000)

// chooseRule returns the first rule whose condition holds for the usage and
// start time, or an unconditional rule. A formula-only entry has no rules and
// returns a zero rule. No holding rule fails with core.CodeUnsupported
// (REQ-MODEL-069, REQ-MODEL-070, REQ-MODEL-073). When a rule's when condition
// errors at runtime (division by zero, missing parameter, non-boolean result),
// pricing fails with that error and later rules are not tried; the spec leaves
// this undefined and the lead chose fail-closed on money paths so a bad rule
// cannot silently fall through to a cheaper one.
func (c *Catalog) chooseRule(entry Price, modelID string, params json.RawMessage, usage model.Usage, at time.Time, tenant core.TenantID) (Rule, error) {
	if len(entry.Rules) == 0 {
		return Rule{}, nil
	}
	for _, rule := range entry.Rules {
		when := strings.TrimSpace(rule.When)
		if when == "" || when == "always" {
			return rule, nil
		}
		ok, err := c.evalCondition(when, modelID, params, usage, rule.Rates, at, tenant)
		if err != nil {
			return Rule{}, err
		}
		if ok {
			return rule, nil
		}
	}
	return Rule{}, unsupportedPrice(modelID)
}

func (c *Catalog) evalCondition(src, modelID string, params json.RawMessage, usage model.Usage, rates Rates, at time.Time, tenant core.TenantID) (bool, error) {
	vars, err := bindings(modelID, at, params, usage, rates, tenant)
	if err != nil {
		return false, err
	}
	program, err := c.program(src)
	if err != nil {
		return false, err
	}
	out, err := program.Eval(vars)
	if err != nil {
		return false, err
	}
	ok, isBool := out.(bool)
	if !isBool {
		return false, amountError(modelID, fmt.Errorf("rate rule condition is not a boolean"))
	}
	return ok, nil
}

func validateUsage(usage model.Usage) error {
	if usage.CachedTokens > usage.InputTokens {
		return core.NewError(core.CodeInvalidArgument, "cached tokens exceed input tokens", nil)
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CachedTokens < 0 || usage.ReasoningTokens < 0 {
		return core.NewError(core.CodeInvalidArgument, "usage token counts must not be negative", nil)
	}
	return nil
}

func ensureNonNegativeAmount(modelID string, amount int64) error {
	if amount < 0 {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("price model %q: amount must not be negative", modelID), nil)
	}
	return nil
}

// amount prices usage with the entry's formula, else the descriptor's, else
// the default token formula over the chosen rule's rates (REQ-MODEL-074).
func (c *Catalog) amount(entry Price, descriptorFormula, modelID string, params json.RawMessage, usage model.Usage, rates Rates, at time.Time, tenant core.TenantID) (int64, error) {
	// Token usage is checked before any formula, the default one or a model's
	// own, so bad provider data cannot turn into a credit (REQ-MODEL-082).
	if err := validateUsage(usage); err != nil {
		return 0, err
	}
	formula := entry.Formula
	if formula == "" {
		formula = descriptorFormula
	}
	var amount int64
	var err error
	if formula == "" {
		amount, err = defaultTokenCost(modelID, rates, usage)
	} else {
		vars, berr := bindings(modelID, at, params, usage, rates, tenant)
		if berr != nil {
			return 0, berr
		}
		program, perr := c.program(formula)
		if perr != nil {
			return 0, perr
		}
		out, eerr := program.Eval(vars)
		if eerr != nil {
			return 0, eerr
		}
		amount, err = amountFromCEL(modelID, out)
	}
	if err != nil {
		return 0, err
	}
	if err := ensureNonNegativeAmount(modelID, amount); err != nil {
		return 0, err
	}
	return amount, nil
}

func (c *Catalog) program(src string) (*expr.Program, error) {
	c.progMu.Lock()
	defer c.progMu.Unlock()
	if p, ok := c.programs[src]; ok {
		return p, nil
	}
	p, err := c.env.Compile(src, nil)
	if err != nil {
		return nil, err
	}
	c.programs[src] = p
	return p, nil
}

func bindings(modelID string, at time.Time, params json.RawMessage, usage model.Usage, rates Rates, tenant core.TenantID) (map[string]any, error) {
	p, err := paramsObject(params)
	if err != nil {
		return nil, amountError(modelID, err)
	}
	return map[string]any{
		"at":     at.UTC().Format(time.RFC3339Nano),
		"params": p,
		"usage": map[string]any{
			"input_tokens":     usage.InputTokens,
			"output_tokens":    usage.OutputTokens,
			"cached_tokens":    usage.CachedTokens,
			"reasoning_tokens": usage.ReasoningTokens,
		},
		"tenant": string(tenant),
		"rates":  ratesObject(rates),
	}, nil
}

func paramsObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("parameters are not JSON")
	}
	if v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parameters are not a JSON object")
	}
	return m, nil
}

func ratesObject(rates Rates) map[string]any {
	out := make(map[string]any, len(rates))
	for k, v := range rates {
		out[k] = v
	}
	return out
}

func missingRate(modelID, kind string) error {
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("model %q has no %s rate", modelID, kind), nil)
}

// defaultTokenCost is the default token formula (REQ-MODEL-074,
// REQ-MODEL-075, REQ-MODEL-079 to REQ-MODEL-081). Rates are per million
// tokens; the total is divided by one million once and rounded half away from
// zero. A positive usage count for a token kind needs a rate for that kind.
func defaultTokenCost(modelID string, rates Rates, usage model.Usage) (int64, error) {
	if usage.OutputTokens > 0 {
		if _, ok := rates["output"]; !ok {
			return 0, missingRate(modelID, "output")
		}
	}
	_, hasCachedRate := rates["cached"]
	_, hasInputRate := rates["input"]
	if hasCachedRate {
		uncached := usage.InputTokens - usage.CachedTokens
		if uncached > 0 && !hasInputRate {
			return 0, missingRate(modelID, "input")
		}
	} else if usage.InputTokens > 0 && !hasInputRate {
		return 0, missingRate(modelID, "input")
	}
	inputRate := rates["input"]
	outputRate := rates["output"]
	total := new(big.Int)
	if cachedRate, ok := rates["cached"]; ok {
		uncached := usage.InputTokens - usage.CachedTokens
		total.Add(total, new(big.Int).Mul(big.NewInt(uncached), big.NewInt(inputRate)))
		total.Add(total, new(big.Int).Mul(big.NewInt(usage.CachedTokens), big.NewInt(cachedRate)))
	} else {
		total.Add(total, new(big.Int).Mul(big.NewInt(usage.InputTokens), big.NewInt(inputRate)))
	}
	total.Add(total, new(big.Int).Mul(big.NewInt(usage.OutputTokens), big.NewInt(outputRate)))
	return divideRounded(modelID, total, million)
}

func amountFromCEL(modelID string, out any) (int64, error) {
	switch v := out.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, amountError(modelID, fmt.Errorf("amount is too large"))
		}
		return int64(v), nil
	case float64:
		return amountFromFloat(modelID, v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i, nil
		}
		return 0, amountError(modelID, fmt.Errorf("formula must return a whole number of milli-points using integer arithmetic"))
	default:
		return 0, amountError(modelID, fmt.Errorf("formula returned %T, want a number", out))
	}
}

// maxFloatMilliPoints is 2^53; larger integers are not exact in float64, so a
// formula that returns a double cannot be trusted at or beyond this magnitude.
const maxFloatMilliPoints = 9007199254740992

func amountFromFloat(modelID string, v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, amountError(modelID, fmt.Errorf("amount is not a number"))
	}
	if v != math.Trunc(v) {
		return 0, amountError(modelID, fmt.Errorf("formula must return a whole number of milli-points using integer arithmetic"))
	}
	if v <= -maxFloatMilliPoints || v >= maxFloatMilliPoints {
		return 0, amountError(modelID, fmt.Errorf("formula must return a whole number of milli-points using integer arithmetic"))
	}
	i := int64(v)
	if float64(i) != v {
		return 0, amountError(modelID, fmt.Errorf("formula must return a whole number of milli-points using integer arithmetic"))
	}
	return i, nil
}

func roundedRat(modelID string, r *big.Rat) (int64, error) {
	// round(num/den) half away from zero.
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	absRem := new(big.Int).Abs(rem)
	absRem.Lsh(absRem, 1)
	if absRem.Cmp(den) >= 0 {
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, amountError(modelID, fmt.Errorf("amount is out of range"))
	}
	return q.Int64(), nil
}

func divideRounded(modelID string, num, den *big.Int) (int64, error) {
	return roundedRat(modelID, new(big.Rat).SetFrac(num, den))
}
