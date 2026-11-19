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
	Expr        *exprWorld
	SQL         *sqlWorld
	Prompt      *promptWorld
	Blob        *blobWorld
	Secrets     *secretsWorld
	Credentials *credentialsWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:   newCoreWorld(),
			Expr:   &exprWorld{bindings: map[string]any{}},
			SQL:    newSQLWorld(),
			Prompt: newPromptWorld(),
			Blob:   newBlobWorld(),
		}), nil
	})
	registerCore(sc)
	registerExpr(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerBlob(sc)
	registerSecrets(sc)
	registerCredentials(sc)
	registerSharedSteps(sc)
}

// registerSharedSteps holds phrases used by more than one domain. An open
// failure is reported by the SQL store and by the Secrets port, and a call
// failure by the SQL store and the Blob port; one handler per phrase reads
// whichever domain opened in the scenario.
func registerSharedSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the call fails with code "([^"]*)"$`, assertCallFailsWithCode)
	sc.Step(`^opening fails with code "([^"]*)"$`, assertOpeningFailsWithCode)
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
	return fmt.Errorf("call succeeded, want %s", code)
}

func assertOpeningFailsWithCode(ctx context.Context, code string) error {
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
}
