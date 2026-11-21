package catalog

import (
	"encoding/json"
	"fmt"
	"slices"
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
	// At is the run clock's time to price at. It is zero when the caller uses
	// the catalog's own clock; a run passes dc.Now() so a replay prices a
	// time-based rule at the same instant (REQ-MODEL-078).
	At time.Time
}

// Quote pins the price entry, the model's formula, the start time, the tenant
// and the parameters that applied when the call started. Retain it and pass it
// to Settle; later price changes do not reprice the call (REQ-MODEL-078). A
// quote is serializable: a durable step records it and every replay settles by
// the pin it held, even when a price layer changed in between.
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

// quoteRecord is the durable form of a Quote. It carries the whole pin: the
// estimate, the entry with its rules and rates, the descriptor formula, the
// start time, the tenant and the priced parameters.
type quoteRecord struct {
	Model             string          `json:"model"`
	Estimate          int64           `json:"estimate"`
	Rule              string          `json:"rule,omitempty"`
	Entry             Price           `json:"entry"`
	DescriptorFormula string          `json:"descriptor_formula,omitempty"`
	At                time.Time       `json:"at"`
	Tenant            core.TenantID   `json:"tenant,omitempty"`
	Params            json.RawMessage `json:"params,omitempty"`
}

// MarshalJSON records the quote's pin so a durable step can store it and a
// replay can settle by the entry that applied when the call started.
func (q Quote) MarshalJSON() ([]byte, error) {
	return json.Marshal(quoteRecord{
		Model:             q.Model,
		Estimate:          q.Estimate,
		Rule:              q.Rule,
		Entry:             q.pinned.entry,
		DescriptorFormula: q.pinned.descriptorFormula,
		At:                q.pinned.at,
		Tenant:            q.pinned.tenant,
		Params:            q.pinned.params,
	})
}

// UnmarshalJSON restores a quote recorded by MarshalJSON. Settle still rejects
// a record with no valid pinned entry, so only a quote produced by Start (or
// restored from one) can settle a call.
func (q *Quote) UnmarshalJSON(data []byte) error {
	var r quoteRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	q.Model = r.Model
	q.Estimate = r.Estimate
	q.Rule = r.Rule
	q.pinned = pinned{
		entry:             r.Entry,
		descriptorFormula: r.DescriptorFormula,
		at:                r.At,
		tenant:            r.Tenant,
		params:            append(json.RawMessage(nil), r.Params...),
	}
	return nil
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

// Price is one model's price in one layer. Rules are tried in order and the
// first whose When holds applies. A blank When (including whitespace only)
// means the rule always holds. A Formula, when set after trimming surrounding
// whitespace, is CEL over params, usage and rates and sets the amount for the
// rule that held; it does not replace rule selection.
type Price struct {
	Formula string `json:"formula,omitempty" yaml:"formula,omitempty"`
	Rules   []Rule `json:"rules,omitempty" yaml:"rules,omitempty"`
}

// Rule is one rate rule. When is CEL over at, params, usage and tenant; blank
// When (including whitespace only) means the rule always holds.
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
func (p Price) valid() bool { return exprSource(p.Formula) != "" || len(p.Rules) > 0 }

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

// validatePrice checks every rate in every rule. Empty prices are deletion
// and need no check.
func validatePrice(modelID string, p Price) error {
	if !p.valid() {
		return nil
	}
	for _, rule := range p.Rules {
		keys := make([]string, 0, len(rule.Rates))
		for key := range rule.Rates {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if rule.Rates[key] < 0 {
				return core.NewError(core.CodeInvalidArgument,
					fmt.Sprintf("model %q: rate %q must not be negative", modelID, key),
					map[string]any{"model": modelID, "rate": key})
			}
		}
	}
	return nil
}

func cloneDescriptor(d model.Descriptor) model.Descriptor {
	cp := d
	cp.Caps = append([]model.Capability(nil), d.Caps...)
	cp.Inputs = append([]model.InputSlot(nil), d.Inputs...)
	cp.Params = append([]byte(nil), d.Params...)
	return cp
}
