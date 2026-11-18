// Package steps holds the shared step library. Steps are thin: they translate
// glossary phrases into calls on the World's drivers and assert on results.
// Add a phrase only together with the scenario that needs it; keep phrases in
// the language of specs/glossary.md.
package steps

import (
	"context"

	"github.com/cucumber/godog"
)

type worldKey struct{}

// World is per-scenario state. Never share it between scenarios.
type World struct {
	Core   *coreWorld
	Prompt *promptWorld
	Expr   *exprWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:   newCoreWorld(),
			Prompt: newPromptWorld(),
			Expr:   &exprWorld{bindings: map[string]any{}},
		}), nil
	})
	registerCore(sc)
	registerPrompt(sc)
	registerExpr(sc)
}
