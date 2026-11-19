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
	Schema      *schemaWorld
	Expr        *exprWorld
	SQL         *sqlWorld
	Prompt      *promptWorld
	Telemetry   *telemetryWorld
	Blob        *blobWorld
	Secrets     *secretsWorld
	Credentials *credentialsWorld
	Flow        *flowWorld
	FlowBuilder *builderWorld
	Catalog     *catalogWorld
	Machine     *machineWorld
	Host        *hostWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:        newCoreWorld(),
			Schema:      newSchemaWorld(),
			Expr:        &exprWorld{bindings: map[string]any{}},
			SQL:         newSQLWorld(),
			Prompt:      newPromptWorld(),
			Telemetry:   newTelemetryWorld(),
			Blob:        newBlobWorld(),
			Secrets:     newSecretsWorld(),
			Flow:        newFlowWorld(),
			FlowBuilder: newBuilderWorld(),
			Catalog:     newCatalogWorld(),
			Machine:     newMachineWorld(),
			Host:        newHostWorld(),
		}), nil
	})
	registerCore(sc)
	registerSchema(sc)
	registerExpr(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerBlob(sc)
	registerTelemetry(sc)
	registerFlowCompile(sc)
	registerFlowLimits(sc)
	registerFlowBuilder(sc)
	registerFlowMachine(sc)
	registerFlowHost(sc)
	registerSharedSteps(sc)
	registerBlobUploads(sc)
	registerSecrets(sc)
	registerOpeningSteps(sc)
	registerCredentials(sc)
	registerCatalog(sc)
}

func registerSharedSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the call fails with code "([^"]*)"$`, assertCallFailsWithCode)
	registerSharedRunOutcomeSteps(sc)
	registerSharedPassStep(sc)
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
