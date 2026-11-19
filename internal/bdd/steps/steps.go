// Package steps holds the shared step library. Steps are thin: they translate
// glossary phrases into calls on the World's drivers and assert on results.
// Add a phrase only together with the scenario that needs it; keep phrases in
// the language of specs/glossary.md.
package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
)

type worldKey struct{}

// World is per-scenario state. Never share it between scenarios.
type World struct {
	Core        *coreWorld
	SQL         *sqlWorld
	Prompt      *promptWorld
	Secrets     *secretsWorld
	Credentials *credentialsWorld
	Schema      *schemaWorld
	Model       *modelWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:   newCoreWorld(),
			Prompt: newPromptWorld(),
			Schema: newSchemaWorld(),
			Model:  newModelWorld(),
		}), nil
	})
	registerCore(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerSecrets(sc)
	registerCredentials(sc)
	registerSchema(sc)
	registerModel(sc)
	registerShared(sc)
}

// registerShared holds phrases used by more than one domain. The SQL store and
// the Secrets port both report an open failure with the same phrase; one
// handler reads whichever domain opened in the scenario.
func registerShared(sc *godog.ScenarioContext) {
	sc.Step(`^opening fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx)
		var err error
		if w.SQL != nil {
			err = w.SQL.openErr
		}
		if err == nil && w.Secrets != nil {
			err = w.Secrets.lastErr
		}
		if err == nil {
			return fmt.Errorf("opening succeeded, want %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %w", got, code, err)
		}
		return nil
	})
}
