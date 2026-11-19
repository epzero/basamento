package steps

import (
	"context"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/platform/telemetry"
)

func registerTelemetrySetup(sc *godog.ScenarioContext) {
	sc.Step(`^an embedded telemetry setup with a fake clock$`, func(ctx context.Context) error {
		return world(ctx).Telemetry.startEmbedded(ctx, world(ctx).Core)
	})
	sc.Step(`^a cloud telemetry setup with a fake clock that exports to a simulated collector$`, func(ctx context.Context) error {
		return world(ctx).Telemetry.startCloud(ctx, world(ctx).Core)
	})

	sc.Step(`^tenant "([^"]*)" runs run "([^"]*)"$`, func(ctx context.Context, tenant, run string) error {
		w := world(ctx).Telemetry
		w.tenant, w.run = tenant, run
		return nil
	})
	sc.Step(`^run "([^"]*)" of tenant "([^"]*)" is running$`, func(ctx context.Context, run, tenant string) error {
		w := world(ctx).Telemetry
		w.tenant, w.run = tenant, run
		return nil
	})
	sc.Step(`^run "([^"]*)" and run "([^"]*)" of tenant "([^"]*)" are running$`, func(ctx context.Context, first, second, tenant string) error {
		w := world(ctx).Telemetry
		w.tenant, w.run = tenant, first
		_ = second
		return nil
	})
	sc.Step(`^the span store retention period is (\d+) hours?$`, func(ctx context.Context, hours int) error {
		w := world(ctx).Telemetry
		setup, err := telemetry.New(telemetry.Config{
			Profile:         telemetry.ProfileEmbedded,
			Clock:           telemetryClock{core: world(ctx).Core},
			Spans:           w.spans,
			RetentionPeriod: time.Duration(hours) * time.Hour,
		})
		if err != nil {
			return err
		}
		w.setup = setup
		return nil
	})

	sc.Step(`^(\d+) milliseconds pass on the clock$`, func(ctx context.Context, ms int) error {
		world(ctx).Core.clock.Advance(time.Duration(ms) * time.Millisecond)
		return nil
	})
	sc.Step(`^the clock advances by (\d+) seconds?$`, func(ctx context.Context, seconds int) error {
		world(ctx).Core.clock.Advance(time.Duration(seconds) * time.Second)
		return nil
	})
	sc.Step(`^(\d+) hours and (\d+) (seconds?|minutes?) pass on the clock and the span store purges expired spans$`, func(ctx context.Context, hours, amount int, unit string) error {
		w := world(ctx).Telemetry
		d := time.Duration(hours) * time.Hour
		if strings.HasPrefix(unit, "minute") {
			d += time.Duration(amount) * time.Minute
		} else {
			d += time.Duration(amount) * time.Second
		}
		world(ctx).Core.clock.Advance(d)
		_, err := w.setup.PurgeSpans(ctx)
		return err
	})

	sc.Step(`^the telemetry setup holds a credential "([^"]*)" with the value "([^"]*)"$`, func(ctx context.Context, name, value string) error {
		world(ctx).Telemetry.setup.SetCredential(value)
		return nil
	})

}
