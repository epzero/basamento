package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
	"github.com/epzero/basamento/model"
)

// Catalog stores descriptors by model id and the three layers of the price
// table. A Catalog is safe for concurrent use.
type Catalog struct {
	clock core.Clock

	mu          sync.RWMutex
	descriptors map[string]model.Descriptor
	snapshot    map[string]Price
	deployment  map[string]Price
	tenants     map[core.TenantID]map[string]Price

	env *expr.Env
	// programs holds one compiled program per distinct expression source ever
	// accepted at set time; nothing is evicted. The number of price entries a
	// deployment can hold is the bound; a future tenant-facing price API must
	// bound it.
	programs map[string]*expr.Program
	progMu   sync.Mutex
}

var pricingVars = []expr.Var{
	{Name: "at", Schema: json.RawMessage(`{"type":"string","format":"date-time"}`)},
	{Name: "params", Schema: json.RawMessage(`{"type":"object","additionalProperties":true}`)},
	{Name: "usage", Schema: json.RawMessage(`{"type":"object","properties":{"input_tokens":{"type":"integer"},"output_tokens":{"type":"integer"},"cached_tokens":{"type":"integer"},"reasoning_tokens":{"type":"integer"}},"additionalProperties":false}`)},
	{Name: "tenant", Schema: json.RawMessage(`{"type":"string"}`)},
	{Name: "rates", Schema: json.RawMessage(`{"type":"object","additionalProperties":{"type":"integer"}}`)},
}

// New returns a catalog with the shipped price snapshot embedded in the
// binary. The clock is read when a call starts and recorded with its quote.
func New(clock core.Clock) (*Catalog, error) {
	return NewWithSnapshot(clock, snapshotYAML)
}

// NewWithSnapshot returns a catalog whose snapshot layer is yamlText instead
// of the bytes embedded in the binary. A deployment build may pass its own
// generated snapshot at startup.
func NewWithSnapshot(clock core.Clock, yamlText []byte) (*Catalog, error) {
	if clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "catalog needs a clock", nil)
	}
	snapshot, err := parseSnapshot(yamlText)
	if err != nil {
		return nil, err
	}
	env, err := expr.NewEnv(pricingVars)
	if err != nil {
		return nil, internalError("build price expression environment", err)
	}
	c := &Catalog{
		clock:       clock,
		descriptors: map[string]model.Descriptor{},
		snapshot:    snapshot,
		deployment:  map[string]Price{},
		tenants:     map[core.TenantID]map[string]Price{},
		env:         env,
		programs:    map[string]*expr.Program{},
	}
	for _, modelID := range slices.Sorted(maps.Keys(snapshot)) {
		if err := c.precompilePrice(modelID, snapshot[modelID]); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// AddDescriptor stores d by its model id, replacing an earlier descriptor for
// the same id.
func (c *Catalog) AddDescriptor(d model.Descriptor) error {
	if d.ID == "" {
		return core.NewError(core.CodeInvalidArgument, "descriptor has no model id", nil)
	}
	if err := c.precompileSource(d.ID, d.Price); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.descriptors[d.ID] = cloneDescriptor(d)
	return nil
}

// Lookup returns the descriptor held for modelID. An id the catalog does not
// hold fails with core.CodeNotFound (REQ-MODEL-067, REQ-MODEL-068).
func (c *Catalog) Lookup(_ context.Context, modelID string) (model.Descriptor, error) {
	c.mu.RLock()
	d, ok := c.descriptors[modelID]
	c.mu.RUnlock()
	if !ok {
		return model.Descriptor{}, core.NewError(core.CodeNotFound, "model not found", map[string]any{"model": modelID})
	}
	return cloneDescriptor(d), nil
}

// SetSnapshotPrice sets modelID's price in the snapshot shipped with the
// binary. An empty Price removes the entry.
func (c *Catalog) SetSnapshotPrice(modelID string, p Price) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setPrice(c.snapshot, modelID, p)
}

// SetDeploymentPrice sets modelID's price in the deployment layer. An empty
// Price removes the entry.
func (c *Catalog) SetDeploymentPrice(modelID string, p Price) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setPrice(c.deployment, modelID, p)
}

// SetTenantPrice sets modelID's price in tenant's layer. An empty Price removes
// the entry. A tenant id is required: a tenantless call never reads a tenant
// layer (REQ-MODEL-072).
func (c *Catalog) SetTenantPrice(tenant core.TenantID, modelID string, p Price) error {
	if tenant == "" {
		return core.NewError(core.CodeInvalidArgument, "tenant required for a tenant price", nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.tenants[tenant]
	if m == nil {
		m = map[string]Price{}
		c.tenants[tenant] = m
	}
	if err := c.setPrice(m, modelID, p); err != nil {
		if len(m) == 0 {
			delete(c.tenants, tenant)
		}
		return err
	}
	if len(m) == 0 {
		delete(c.tenants, tenant)
	}
	return nil
}

func (c *Catalog) setPrice(layer map[string]Price, modelID string, p Price) error {
	if !p.valid() {
		delete(layer, modelID)
		return nil
	}
	if err := validatePrice(modelID, p); err != nil {
		return err
	}
	if err := c.precompilePrice(modelID, p); err != nil {
		return err
	}
	layer[modelID] = p.clone()
	return nil
}

func (c *Catalog) precompilePrice(modelID string, p Price) error {
	if err := c.precompileSource(modelID, p.Formula); err != nil {
		return err
	}
	for _, rule := range p.Rules {
		when := exprSource(rule.When)
		if when == "" || when == "always" {
			continue
		}
		if err := c.precompileSource(modelID, when); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) precompileSource(modelID, src string) error {
	src = exprSource(src)
	if src == "" {
		return nil
	}
	c.progMu.Lock()
	defer c.progMu.Unlock()
	if _, ok := c.programs[src]; ok {
		return nil
	}
	p, err := c.env.Compile(src, nil)
	if err != nil {
		return amountError(modelID, err)
	}
	c.programs[src] = p
	return nil
}

// Start prices a call before it starts, records the start time and returns the
// pinned quote. Its estimate is the model's formula on the counted input
// tokens and the output limit (REQ-MODEL-077). A model with no price in any
// layer, or whose chosen layer has no rule that holds, fails with
// core.CodeUnsupported (REQ-MODEL-073).
func (c *Catalog) Start(ctx context.Context, req Request) (Quote, error) {
	if req.Model == "" {
		return Quote{}, core.NewError(core.CodeInvalidArgument, "request has no model", nil)
	}
	at := c.clock.Now().UTC()
	tenant, _ := core.TenantFrom(ctx)
	entry, descriptorFormula, ok := c.priceFor(tenant, req.Model)
	if !ok {
		return Quote{}, unsupportedPrice(req.Model)
	}
	usage := model.Usage{InputTokens: req.InputTokens, OutputTokens: req.OutputLimit}
	if err := validateUsage(req.Model, usage); err != nil {
		return Quote{}, err
	}
	rule, err := c.chooseRule(entry, req.Model, req.Params, usage, at, tenant)
	if err != nil {
		return Quote{}, err
	}
	amount, err := c.amount(entry, descriptorFormula, req.Model, req.Params, usage, rule.Rates, at, tenant)
	if err != nil {
		return Quote{}, err
	}
	return Quote{
		Model:    req.Model,
		Estimate: amount,
		Rule:     rule.When,
		pinned: pinned{
			entry:             entry,
			descriptorFormula: descriptorFormula,
			at:                at,
			tenant:            tenant,
			params:            append(json.RawMessage(nil), req.Params...),
		},
	}, nil
}

// Estimate returns the reserved cost without keeping the quote. Callers that
// settle a call use Start instead.
func (c *Catalog) Estimate(ctx context.Context, req Request) (int64, error) {
	q, err := c.Start(ctx, req)
	if err != nil {
		return 0, err
	}
	return q.Estimate, nil
}

// Settle prices a started call's actual usage with the price entry and start
// time its quote captured (REQ-MODEL-078). Usage that reports more cached
// tokens than input tokens fails with core.CodeInvalidArgument before any
// formula runs (REQ-MODEL-082).
func (c *Catalog) Settle(q Quote, usage model.Usage) (Charge, error) {
	if q.Model == "" {
		return Charge{}, core.NewError(core.CodeInvalidArgument, "quote has no model", nil)
	}
	if !q.pinned.entry.valid() {
		return Charge{}, core.NewError(core.CodeInvalidArgument, "quote was not produced by Start", nil)
	}
	if err := validateUsage(q.Model, usage); err != nil {
		return Charge{}, err
	}
	rule, err := c.chooseRule(q.pinned.entry, q.Model, q.pinned.params, usage, q.pinned.at, q.pinned.tenant)
	if err != nil {
		return Charge{}, err
	}
	amount, err := c.amount(q.pinned.entry, q.pinned.descriptorFormula, q.Model, q.pinned.params, usage, rule.Rates, q.pinned.at, q.pinned.tenant)
	if err != nil {
		return Charge{}, err
	}
	return Charge{MilliPoints: amount, Rule: rule.When}, nil
}

// priceFor returns the most specific entry for modelID and the descriptor's
// formula captured with it. A tenant entry is read only when the call carries
// a tenant (REQ-MODEL-072).
func (c *Catalog) priceFor(tenant core.TenantID, modelID string) (Price, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if tenant != "" {
		if p, ok := c.tenants[tenant][modelID]; ok && p.valid() {
			return p.clone(), descriptorFormula(c.descriptors, modelID), true
		}
	}
	if p, ok := c.deployment[modelID]; ok && p.valid() {
		return p.clone(), descriptorFormula(c.descriptors, modelID), true
	}
	if p, ok := c.snapshot[modelID]; ok && p.valid() {
		return p.clone(), descriptorFormula(c.descriptors, modelID), true
	}
	return Price{}, "", false
}

func descriptorFormula(descriptors map[string]model.Descriptor, modelID string) string {
	d, ok := descriptors[modelID]
	if !ok {
		return ""
	}
	return d.Price
}

func unsupportedPrice(modelID string) error {
	return core.NewError(core.CodeUnsupported, "no price for model", map[string]any{"model": modelID})
}

func internalError(what string, err error) error {
	e := core.NewError(core.CodeInternal, what, nil)
	e.Cause = err
	return e
}

func amountError(modelID string, err error) error {
	e := core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("price model %q: %v", modelID, err),
		map[string]any{"model": modelID})
	e.Cause = err
	return e
}
