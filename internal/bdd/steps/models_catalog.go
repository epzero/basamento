package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
)

type catalogWorld struct {
	catalog *catalog.Catalog
	held    map[string]model.Descriptor

	lookup    model.Descriptor
	lookupErr error

	request  catalog.Request
	params   map[string]string
	estimate int64
	charge   catalog.Charge
	quotes   map[string]catalog.Quote
	charges  map[string]catalog.Charge
	lastErr  error
}

func newCatalogWorld() *catalogWorld {
	return &catalogWorld{
		held:    map[string]model.Descriptor{},
		quotes:  map[string]catalog.Quote{},
		charges: map[string]catalog.Charge{},
	}
}

// scenarioClock reads the scenario's fake clock on every call, so a step that
// replaces it ("the clock reads") is visible to the catalog.
type scenarioClock struct{ w *World }

func (c scenarioClock) Now() time.Time { return c.w.Core.clock.Now() }

func (w *catalogWorld) cat(ctx context.Context) (*catalog.Catalog, error) {
	if w.catalog != nil {
		return w.catalog, nil
	}
	c, err := catalog.New(scenarioClock{w: world(ctx)})
	if err != nil {
		return nil, err
	}
	w.catalog = c
	for _, d := range w.held {
		if err := c.AddDescriptor(d); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (w *catalogWorld) hold(ctx context.Context, capability, id string) error {
	c, err := w.cat(ctx)
	if err != nil {
		return err
	}
	d := model.Descriptor{
		ID:       id,
		Provider: "sim",
		Caps:     []model.Capability{model.Capability(capability)},
		Protocol: model.ProtocolStream,
		Locality: model.LocalityCloud,
	}
	if err := c.AddDescriptor(d); err != nil {
		return err
	}
	w.held[id] = d
	return nil
}

func (w *catalogWorld) update(ctx context.Context, id string, change func(*model.Descriptor)) error {
	c, err := w.cat(ctx)
	if err != nil {
		return err
	}
	d, ok := w.held[id]
	if !ok {
		return fmt.Errorf("catalog does not hold model %q", id)
	}
	change(&d)
	if err := c.AddDescriptor(d); err != nil {
		return err
	}
	w.held[id] = d
	return nil
}

func registerCatalog(sc *godog.ScenarioContext) {
	sc.Step(`^the catalog holds a "([^"]*)" model "([^"]*)"$`, func(ctx context.Context, capability, id string) error {
		return world(ctx).Catalog.hold(ctx, capability, id)
	})
	sc.Step(`^the catalog holds a "([^"]*)" model "([^"]*)" whose rates hold only when "([^"]*)"$`, func(ctx context.Context, capability, id, when string) error {
		w := world(ctx).Catalog
		if err := w.hold(ctx, capability, id); err != nil {
			return err
		}
		c, err := w.cat(ctx)
		if err != nil {
			return err
		}
		c.SetSnapshotPrice(id, catalog.Price{Rules: []catalog.Rule{{
			When:  when,
			Rates: catalog.Rates{"input": 1, "output": 8},
		}}})
		return nil
	})
	sc.Step(`^the catalog holds a "([^"]*)" model "([^"]*)" with no price in any layer$`, func(ctx context.Context, capability, id string) error {
		return world(ctx).Catalog.hold(ctx, capability, id)
	})

	sc.Step(`^the model "([^"]*)" is priced by the shipped snapshot at "([^"]*)" milli-points$`, func(ctx context.Context, id, formula string) error {
		return world(ctx).Catalog.setSnapshotFormula(ctx, id, formula)
	})
	sc.Step(`^the shipped snapshot then prices "([^"]*)" at "([^"]*)" milli-points$`, func(ctx context.Context, id, formula string) error {
		return world(ctx).Catalog.setSnapshotFormula(ctx, id, formula)
	})
	sc.Step(`^the deployment overrides the price of "([^"]*)" at "([^"]*)" milli-points$`, func(ctx context.Context, id, formula string) error {
		c, err := world(ctx).Catalog.cat(ctx)
		if err != nil {
			return err
		}
		c.SetDeploymentPrice(id, catalog.Price{Formula: formula})
		return nil
	})
	sc.Step(`^tenant "([^"]*)" overrides the price of "([^"]*)" at "([^"]*)" milli-points$`, func(ctx context.Context, tenant, id, formula string) error {
		c, err := world(ctx).Catalog.cat(ctx)
		if err != nil {
			return err
		}
		return c.SetTenantPrice(core.TenantID(tenant), id, catalog.Price{Formula: formula})
	})
	sc.Step(`^tenant "([^"]*)" has no override for the price of "([^"]*)"$`, func(ctx context.Context, tenant, id string) error {
		c, err := world(ctx).Catalog.cat(ctx)
		if err != nil {
			return err
		}
		return c.SetTenantPrice(core.TenantID(tenant), id, catalog.Price{})
	})

	sc.Step(`^the model "([^"]*)" has the rate rules:$`, func(ctx context.Context, id string, table *godog.Table) error {
		rules, err := parseRuleTable(table)
		if err != nil {
			return err
		}
		c, err := world(ctx).Catalog.cat(ctx)
		if err != nil {
			return err
		}
		c.SetSnapshotPrice(id, catalog.Price{Rules: rules})
		return nil
	})
	sc.Step(`^tenant "([^"]*)" overrides the rate rules of "([^"]*)" with:$`, func(ctx context.Context, tenant, id string, table *godog.Table) error {
		rules, err := parseRuleTable(table)
		if err != nil {
			return err
		}
		c, err := world(ctx).Catalog.cat(ctx)
		if err != nil {
			return err
		}
		return c.SetTenantPrice(core.TenantID(tenant), id, catalog.Price{Rules: rules})
	})
	sc.Step(`^the model "([^"]*)" has the rate rules "([^"]*)" per million tokens$`, func(ctx context.Context, id, phrase string) error {
		rates, err := parseRates(phrase)
		if err != nil {
			return err
		}
		c, err := world(ctx).Catalog.cat(ctx)
		if err != nil {
			return err
		}
		c.SetSnapshotPrice(id, catalog.Price{Rules: []catalog.Rule{{Rates: rates}}})
		return nil
	})

	sc.Step(`^the descriptor of "([^"]*)" declares the input slots "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, id, first, second string) error {
		return world(ctx).Catalog.update(ctx, id, func(d *model.Descriptor) {
			d.Inputs = []model.InputSlot{
				{Name: first, Media: mediaOf(first), Required: true},
				{Name: second, Media: mediaOf(second)},
			}
		})
	})
	sc.Step(`^the descriptor of "([^"]*)" declares the vendor protocol "([^"]*)" and the locality "([^"]*)"$`, func(ctx context.Context, id, protocol, locality string) error {
		return world(ctx).Catalog.update(ctx, id, func(d *model.Descriptor) {
			d.Protocol = model.Protocol(protocol)
			d.Locality = model.Locality(locality)
		})
	})

	sc.Step(`^the catalog looks up "([^"]*)"$`, func(ctx context.Context, id string) error {
		w := world(ctx).Catalog
		c, err := w.cat(ctx)
		if err != nil {
			return err
		}
		w.lookup, w.lookupErr = c.Lookup(ctx, id)
		return nil
	})
	sc.Step(`^the catalog returns a descriptor whose capability is "([^"]*)", with input slots "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, capability, first, second string) error {
		w := world(ctx).Catalog
		if w.lookupErr != nil {
			return fmt.Errorf("lookup failed: %w", w.lookupErr)
		}
		d := w.lookup
		if len(d.Caps) != 1 || string(d.Caps[0]) != capability {
			return fmt.Errorf("capabilities = %v, want [%s]", d.Caps, capability)
		}
		if len(d.Inputs) != 2 || d.Inputs[0].Name != first || d.Inputs[1].Name != second {
			return fmt.Errorf("input slots = %v, want %s then %s", d.Inputs, first, second)
		}
		return nil
	})
	sc.Step(`^the descriptor declares the vendor protocol "([^"]*)" and the locality "([^"]*)"$`, func(ctx context.Context, protocol, locality string) error {
		d := world(ctx).Catalog.lookup
		if string(d.Protocol) != protocol || string(d.Locality) != locality {
			return fmt.Errorf("protocol/locality = %s/%s, want %s/%s", d.Protocol, d.Locality, protocol, locality)
		}
		return nil
	})
	sc.Step(`^the lookup fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := world(ctx).Catalog.lookupErr
		if err == nil {
			return fmt.Errorf("lookup succeeded, want code %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %w", got, code, err)
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" makes a call of "([^"]*)" with (\d+) input tokens and an output limit of (\d+) tokens$`, func(ctx context.Context, tenant, id string, input, limit int64) error {
		return world(ctx).Catalog.estimateFor(ctx, core.WithTenant(context.Background(), core.TenantID(tenant)), id, input, limit, nil)
	})
	sc.Step(`^a call of "([^"]*)" outside any tenant runs with (\d+) counted input tokens and an output limit of (\d+) tokens$`, func(ctx context.Context, id string, input, limit int64) error {
		return world(ctx).Catalog.estimateFor(ctx, context.Background(), id, input, limit, nil)
	})
	sc.Step(`^the price table estimates a call of "([^"]*)" with (\d+) counted input tokens and an output limit of (\d+) tokens$`, func(ctx context.Context, id string, input, limit int64) error {
		return world(ctx).Catalog.estimateFor(ctx, context.Background(), id, input, limit, nil)
	})
	sc.Step(`^the price table estimates a call of "([^"]*)" in tenant "([^"]*)" with (\d+) counted input tokens and an output limit of (\d+) tokens$`, func(ctx context.Context, id, tenant string, input, limit int64) error {
		return world(ctx).Catalog.estimateFor(ctx, core.WithTenant(context.Background(), core.TenantID(tenant)), id, input, limit, nil)
	})
	sc.Step(`^a request of "([^"]*)" whose counted input is (\d+) tokens with parameter "([^"]*)" set to "([^"]*)"$`, func(ctx context.Context, id string, input int64, name, value string) error {
		w := world(ctx).Catalog
		w.request = catalog.Request{Model: id, InputTokens: input}
		w.params = map[string]string{name: value}
		return nil
	})
	sc.Step(`^the price table estimates that request with an output limit of (\d+) tokens$`, func(ctx context.Context, limit int64) error {
		w := world(ctx).Catalog
		params, err := json.Marshal(w.params)
		if err != nil {
			return err
		}
		w.request.OutputLimit = limit
		w.request.Params = params
		return w.runEstimate(ctx, context.Background(), w.request)
	})
	sc.Step(`^(\d+) milli-points are estimated for the call$`, func(ctx context.Context, want int64) error {
		w := world(ctx).Catalog
		if w.lastErr != nil {
			return fmt.Errorf("estimate failed: %w", w.lastErr)
		}
		if w.estimate != want {
			return fmt.Errorf("estimate = %d, want %d", w.estimate, want)
		}
		return nil
	})
	sc.Step(`^pricing fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := world(ctx).Catalog.lastErr
		if err == nil {
			return fmt.Errorf("pricing succeeded, want code %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %w", got, code, err)
		}
		return nil
	})

	sc.Step(`^a call of "([^"]*)" spends (\d+) input tokens and (\d+) output tokens(?:, of which (.+))?$`, func(ctx context.Context, id string, input, output int64, detail string) error {
		usage, err := parseUsage(input, output, detail)
		if err != nil {
			return err
		}
		return world(ctx).Catalog.spend(ctx, id, usage)
	})
	sc.Step(`^(\d+) milli-points? (?:are|is) charged for the call$`, func(ctx context.Context, want int64) error {
		w := world(ctx).Catalog
		if w.lastErr != nil {
			return fmt.Errorf("pricing failed: %w", w.lastErr)
		}
		if w.charge.MilliPoints != want {
			return fmt.Errorf("charge = %d, want %d", w.charge.MilliPoints, want)
		}
		return nil
	})
	sc.Step(`^the call is priced at the rule "([^"]*)" and costs (\d+) milli-points$`, func(ctx context.Context, rule string, want int64) error {
		w := world(ctx).Catalog
		if w.lastErr != nil {
			return fmt.Errorf("pricing failed: %w", w.lastErr)
		}
		if w.charge.Rule != rule {
			return fmt.Errorf("rule = %q, want %q", w.charge.Rule, rule)
		}
		if w.charge.MilliPoints != want {
			return fmt.Errorf("charge = %d, want %d", w.charge.MilliPoints, want)
		}
		return nil
	})

	sc.Step(`^a call "([^"]*)" of "([^"]*)" started with (\d+) counted input tokens and an output limit of (\d+) tokens$`, func(ctx context.Context, name, id string, input, limit int64) error {
		return world(ctx).Catalog.startCall(ctx, name, id, input, limit)
	})
	sc.Step(`^the price table prices the settled usage of call "([^"]*)" as (\d+) input tokens and (\d+) output tokens$`, func(ctx context.Context, name string, input, output int64) error {
		return world(ctx).Catalog.settleCall(ctx, name, model.Usage{InputTokens: input, OutputTokens: output})
	})
	sc.Step(`^(\d+) milli-points were estimated for call "([^"]*)"$`, func(ctx context.Context, want int64, name string) error {
		w := world(ctx).Catalog
		q, ok := w.quotes[name]
		if !ok {
			return fmt.Errorf("no call %q", name)
		}
		if q.Estimate != want {
			return fmt.Errorf("estimate of %q = %d, want %d", name, q.Estimate, want)
		}
		return nil
	})
	sc.Step(`^(\d+) milli-points are charged for call "([^"]*)"$`, func(ctx context.Context, want int64, name string) error {
		w := world(ctx).Catalog
		charge, ok := w.charges[name]
		if !ok {
			return fmt.Errorf("no settled call %q", name)
		}
		if charge.MilliPoints != want {
			return fmt.Errorf("charge of %q = %d, want %d", name, charge.MilliPoints, want)
		}
		return nil
	})
	sc.Step(`^call "([^"]*)" was estimated at (\d+) milli-points?$`, func(ctx context.Context, name string, want int64) error {
		w := world(ctx).Catalog
		q, ok := w.quotes[name]
		if !ok {
			return fmt.Errorf("no call %q", name)
		}
		if q.Estimate != want {
			return fmt.Errorf("estimate of %q = %d, want %d", name, q.Estimate, want)
		}
		return nil
	})

	sc.Step(`^the clock reads "([^"]*)"$`, func(ctx context.Context, at string) error {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return err
		}
		world(ctx).Core.clock = coretest.NewFakeClock(t)
		return nil
	})
}

func (w *catalogWorld) setSnapshotFormula(ctx context.Context, id, formula string) error {
	c, err := w.cat(ctx)
	if err != nil {
		return err
	}
	c.SetSnapshotPrice(id, catalog.Price{Formula: formula})
	return nil
}

func (w *catalogWorld) estimateFor(scenarioCtx, callCtx context.Context, id string, input, limit int64, params json.RawMessage) error {
	return w.runEstimate(scenarioCtx, callCtx, catalog.Request{Model: id, Params: params, InputTokens: input, OutputLimit: limit})
}

func (w *catalogWorld) runEstimate(scenarioCtx, callCtx context.Context, req catalog.Request) error {
	c, err := w.cat(scenarioCtx)
	if err != nil {
		return err
	}
	w.lastErr = nil
	w.estimate, w.lastErr = c.Estimate(callCtx, req)
	return nil
}

func (w *catalogWorld) spend(ctx context.Context, id string, usage model.Usage) error {
	c, err := w.cat(ctx)
	if err != nil {
		return err
	}
	q, err := c.Start(ctx, catalog.Request{Model: id, InputTokens: usage.InputTokens, OutputLimit: usage.OutputTokens})
	if err != nil {
		w.lastErr = err
		return nil
	}
	w.charge, w.lastErr = c.Settle(q, usage)
	return nil
}

func (w *catalogWorld) startCall(ctx context.Context, name, id string, input, limit int64) error {
	c, err := w.cat(ctx)
	if err != nil {
		return err
	}
	q, err := c.Start(ctx, catalog.Request{Model: id, InputTokens: input, OutputLimit: limit})
	if err != nil {
		w.lastErr = err
		return nil
	}
	w.quotes[name] = q
	return nil
}

func (w *catalogWorld) settleCall(ctx context.Context, name string, usage model.Usage) error {
	c, err := w.cat(ctx)
	if err != nil {
		return err
	}
	q, ok := w.quotes[name]
	if !ok {
		return fmt.Errorf("no call %q", name)
	}
	charge, err := c.Settle(q, usage)
	if err != nil {
		w.lastErr = err
		return nil
	}
	w.charges[name] = charge
	return nil
}

func parseRuleTable(table *godog.Table) ([]catalog.Rule, error) {
	if len(table.Rows) == 0 {
		return nil, fmt.Errorf("rate rule table is empty")
	}
	index := map[string]int{}
	for i, cell := range table.Rows[0].Cells {
		index[strings.TrimSpace(cell.Value)] = i
	}
	whenAt, ok := index["when"]
	if !ok {
		return nil, fmt.Errorf("rate rule table has no when column")
	}
	var rules []catalog.Rule
	for _, row := range table.Rows[1:] {
		when := ""
		if whenAt < len(row.Cells) {
			when = strings.TrimSpace(row.Cells[whenAt].Value)
		}
		if when == "always" {
			when = ""
		}
		rates := catalog.Rates{}
		for _, name := range []string{"input", "output", "cached"} {
			i, ok := index[name]
			if !ok || i >= len(row.Cells) {
				continue
			}
			raw := strings.TrimSpace(row.Cells[i].Value)
			if raw == "" {
				continue
			}
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("rate %s %q: %w", name, raw, err)
			}
			rates[name] = n
		}
		rules = append(rules, catalog.Rule{When: when, Rates: rates})
	}
	return rules, nil
}

var ratePair = regexp.MustCompile(`(input|output|cached)\s+(\d+)`)

func parseRates(phrase string) (catalog.Rates, error) {
	matches := ratePair.FindAllStringSubmatch(phrase, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("rate phrase %q has no rates", phrase)
	}
	rates := catalog.Rates{}
	for _, m := range matches {
		n, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			return nil, err
		}
		rates[m[1]] = n
	}
	return rates, nil
}

var (
	cachedDetail    = regexp.MustCompile(`(\d+)\s+(?:tokens\s+)?are cached`)
	reasoningDetail = regexp.MustCompile(`(\d+)\s+are reasoning`)
)

func parseUsage(input, output int64, detail string) (model.Usage, error) {
	usage := model.Usage{InputTokens: input, OutputTokens: output}
	if detail == "" {
		return usage, nil
	}
	if m := cachedDetail.FindStringSubmatch(detail); len(m) == 2 {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return model.Usage{}, err
		}
		usage.CachedTokens = n
	}
	if m := reasoningDetail.FindStringSubmatch(detail); len(m) == 2 {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return model.Usage{}, err
		}
		usage.ReasoningTokens = n
	}
	return usage, nil
}

func mediaOf(slot string) string {
	switch slot {
	case "messages":
		return "messages"
	default:
		return "text"
	}
}
