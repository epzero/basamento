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
	Core    *coreWorld
	SQL     *sqlWorld
	Prompt  *promptWorld
	Blob    *blobWorld
	Secrets *secretsWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:    newCoreWorld(),
			SQL:     newSQLWorld(),
			Prompt:  newPromptWorld(),
			Blob:    newBlobWorld(),
			Secrets: newSecretsWorld(),
		}), nil
	})
	registerCore(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerBlob(sc)
	registerSecrets(sc)
	registerOpeningSteps(sc)
	registerSharedSteps(sc)
}

func registerSharedSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the call fails with code "([^"]*)"$`, assertCallFailsWithCode)
}

func assertCallFailsWithCode(ctx context.Context, code string) error {
	w := world(ctx)
	if w.SQL != nil {
		err := w.SQL.lastErr
		if err == nil {
			err = w.SQL.lastTxErr
		}
		if err != nil {
			if string(core.CodeOf(err)) != code {
				return fmt.Errorf("got code %s, want %s", core.CodeOf(err), code)
			}
			return nil
		}
	}
	if w.Blob != nil && (w.Blob.lastErr != nil || w.Blob.openErr != nil) {
		return assertBlobCallCode(ctx, code)
	}
	if w.Secrets != nil && w.Secrets.lastErr != nil {
		if string(core.CodeOf(w.Secrets.lastErr)) != code {
			return fmt.Errorf("got code %s, want %s", core.CodeOf(w.Secrets.lastErr), code)
		}
		return nil
	}
	return fmt.Errorf("call succeeded, want %s", code)
}
