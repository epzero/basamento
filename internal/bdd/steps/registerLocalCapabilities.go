package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerLocalCapabilities(sc *godog.ScenarioContext) {
	// Capabilities.
	sc.Step(`^the runtime is "local"$`, func(ctx context.Context) error {
		localStepWorld(ctx).runtime(ctx)
		return nil
	})
	sc.Step(`^the runtime reports capability "([^"]*)" as (true|false)$`, func(ctx context.Context, name, want string) error {
		caps := localStepWorld(ctx).runtime(ctx).Capabilities()
		values := map[string]bool{
			"durable":           caps.Durable,
			"sync_gate_updates": caps.SyncGateUpdates,
			"continue_as_new":   caps.ContinueAsNew,
			"polyglot_queues":   caps.PolyglotQueues,
		}
		got, ok := values[name]
		if !ok {
			return fmt.Errorf("unknown capability %q", name)
		}
		if fmt.Sprintf("%t", got) != want {
			return fmt.Errorf("capability %q is %t, want %s", name, got, want)
		}
		return nil
	})

}
