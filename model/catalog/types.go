package catalog

import (
	"encoding/json"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// Request is what the price table needs to price a call before it starts:
// the model, its parameters, its counted input tokens and its output limit.
type Request struct {
	Model       string
	Params      json.RawMessage
	InputTokens int64
	OutputLimit int64
}

// Quote pins the price entry, the model's formula, the start time, the tenant
// and the parameters that applied when the call started. Retain it and pass it
// to Settle; later price changes do not reprice the call (REQ-MODEL-078).
type Quote struct {
	// Model is the id the quote was taken for.
	Model string
	// Estimate is the reserved cost in whole milli-points.
	Estimate int64
	// Rule is the when condition of the chosen rate rule; empty means an
	// unconditional rule or a formula with no rules.
	Rule string

	pinned pinned
}

// Charge is what a settled call costs.
type Charge struct {
	// MilliPoints is the cost in whole milli-points.
	MilliPoints int64
	// Rule is the when condition of the rate rule that held at settlement;
	// empty means an unconditional rule or a formula with no rules.
	Rule string
}

type pinned struct {
	entry             Price
	descriptorFormula string
	at                time.Time
	tenant            core.TenantID
	params            json.RawMessage
}

// Price is one model's price in one layer. Formula, when set, is CEL over
// params, usage and rates and wins over Rules. Rules are tried in order and
// the first whose When holds applies; an empty When always holds.
type Price struct {
	Formula string `json:"formula,omitempty" yaml:"formula,omitempty"`
	Rules   []Rule `json:"rules,omitempty" yaml:"rules,omitempty"`
}

// Rule is one rate rule. When is CEL over at, params, usage and tenant; empty
// means the rule always holds.
type Rule struct {
	When  string `json:"when,omitempty" yaml:"when,omitempty"`
	Rates Rates  `json:"rates,omitempty" yaml:"rates,omitempty"`
}

// Rates are milli-points per million units. The token formula reads the
// "input" and "output" keys, and "cached" when a rule defines it; a model's
// own formula may read any key.
type Rates map[string]int64

// valid reports whether the price is an entry. An empty price is a layer with
// no entry for the model.
func (p Price) valid() bool { return p.Formula != "" || len(p.Rules) > 0 }

func (p Price) clone() Price {
	cp := Price{Formula: p.Formula}
	if len(p.Rules) > 0 {
		cp.Rules = make([]Rule, len(p.Rules))
		for i, r := range p.Rules {
			cp.Rules[i] = Rule{When: r.When, Rates: r.Rates.clone()}
		}
	}
	return cp
}

func (r Rates) clone() Rates {
	if r == nil {
		return nil
	}
	cp := make(Rates, len(r))
	for k, v := range r {
		cp[k] = v
	}
	return cp
}

func cloneDescriptor(d model.Descriptor) model.Descriptor {
	cp := d
	cp.Caps = append([]model.Capability(nil), d.Caps...)
	cp.Inputs = append([]model.InputSlot(nil), d.Inputs...)
	cp.Params = append([]byte(nil), d.Params...)
	return cp
}
