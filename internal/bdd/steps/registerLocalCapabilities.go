package steps

import (
	"context"

	"github.com/cucumber/godog"
)

func registerLocalCapabilities(sc *godog.ScenarioContext) {
	// Capabilities.
	sc.Step(`^the runtime is "(local|temporal)"$`, func(ctx context.Context, kind string) error {
		if kind == "temporal" {
			return temporalW(ctx).ensureRuntime()
		}
		localStepWorld(ctx).runtime(ctx)
		return nil
	})
	sc.Step(`^the runtime reports capability "([^"]*)" as (true|false)$`, func(ctx context.Context, name, want string) error {
		if world(ctx).Temporal != nil {
			w := temporalW(ctx)
			if err := w.ensureRuntime(); err != nil {
				return err
			}
			return checkRuntimeCapability(w.rt.Capabilities(), name, want)
		}
		return checkRuntimeCapability(localStepWorld(ctx).runtime(ctx).Capabilities(), name, want)
	})

}
