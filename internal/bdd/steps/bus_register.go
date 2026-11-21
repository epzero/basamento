package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
)

func registerBus(sc *godog.ScenarioContext) {
	registerFailureSource(callFailurePhrase, "bus", busCallFailure)
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		if !scenarioUsesBus(scn) {
			return ctx, nil
		}
		w := world(ctx)
		w.Bus = newBusWorld()
		if err := skipBusByProfile(scn, w.Bus.backend); err != nil {
			return ctx, err
		}
		if strings.Contains(scn.Uri, "specs/bus/assets") && w.Bus.backend != "cloud" {
			return ctx, godog.ErrSkip
		}
		if strings.Contains(scn.Uri, "specs/bus/embedded") && w.Bus.backend != "embedded" {
			return ctx, godog.ErrSkip
		}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Bus; w != nil {
			w.teardown()
		}
		return ctx, nil
	})
	registerBusPublish(sc)
	registerBusKV(sc)
	registerBusLimits(sc)
	registerBusOpen(sc)
	registerBusAssets(sc)
}

func busCallFailure(ctx context.Context, code string) (bool, error) {
	w := world(ctx).Bus
	if w == nil || w.lastErr == nil {
		return false, nil
	}
	if string(core.CodeOf(w.lastErr)) != code {
		return true, fmt.Errorf("got code %s, want %s", core.CodeOf(w.lastErr), code)
	}
	return true, nil
}
