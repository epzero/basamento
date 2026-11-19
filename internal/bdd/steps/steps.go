// Package steps holds the shared step library. Steps are thin: they translate
// glossary phrases into calls on the World's drivers and assert on results.
// Add a phrase only together with the scenario that needs it; keep phrases in
// the language of specs/glossary.md.
package steps

import (
	"context"
	"fmt"
	"strings"

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
	Bus         *busWorld
	Secrets     *secretsWorld
	Config      *configWorld
	Credentials *credentialsWorld
	Schema      *schemaWorld
	Model       *modelWorld
	Catalog     *catalogWorld
	Node        *nodeWorld
	Providers   *providerWorld
	Flow        *flowWorld
	Machine     *machineWorld
	Host        *hostWorld
	Local       *localWorld
	Temporal    *temporalWorld
	Eventlog    *eventlogWorld
	Stream      *streamWorld
	Auth        *authWorld
	Authz       *authzWorld
	API         *apiWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:        newCoreWorld(),
			Expr:        &exprWorld{bindings: map[string]any{}},
			SQL:         newSQLWorld(),
			Prompt:      newPromptWorld(),
			Blob:        newBlobWorld(),
			Bus:         newBusWorld(),
			Secrets:     newSecretsWorld(),
			Config:      newConfigWorld(),
			Credentials: newCredentialsWorld(),
			Schema:      newSchemaWorld(),
			Model:       newModelWorld(),
			Catalog:     newCatalogWorld(),
			Node:        newNodeWorld(),
			Providers:   &providerWorld{},
			Flow:        newFlowWorld(),
			Machine:     newMachineWorld(),
			Host:        newHostWorld(),
			Local:       newLocalWorld(),
			Temporal:    newTemporalWorld(),
			Eventlog:    newEventlogWorld(),
			Stream:      newStreamWorld(),
			Auth:        newAuthWorld(),
			Authz:       newAuthzWorld(),
			API:         newAPIWorld(),
		}), nil
	})
	registerCore(sc)
	registerExpr(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerBlob(sc)
	registerSecrets(sc)
	registerConfig(sc)
	registerConfigServe(sc)
	registerOpeningSteps(sc)
	registerCredentials(sc)
	registerBus(sc)
	registerSchema(sc)
	registerModel(sc)
	registerCatalog(sc)
	registerNode(sc)
	registerProviders(sc)
	registerFlowCompile(sc)
	registerFlowMachine(sc)
	registerFlowHost(sc)
	registerLocal(sc)
	registerTemporal(sc)
	registerEmbedded(sc)
	registerEventlog(sc)
	registerStreamLive(sc)
	registerAuth(sc)
	registerAuthz(sc)
	registerAPI(sc)
	registerSharedSteps(sc)
	registerShared(sc)
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		world(ctx).Config.teardown()
		world(ctx).API.teardown()
		return ctx, nil
	})
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
	if w.Bus != nil && w.Bus.lastErr != nil {
		if string(core.CodeOf(w.Bus.lastErr)) != code {
			return fmt.Errorf("got code %s, want %s", core.CodeOf(w.Bus.lastErr), code)
		}
		return nil
	}
	if w.Blob != nil && (w.Blob.lastErr != nil || w.Blob.openErr != nil) {
		return assertBlobCallCode(ctx, code)
	}
	if w.Secrets != nil && (w.Secrets.lastErr != nil) {
		if string(core.CodeOf(w.Secrets.lastErr)) != code {
			return fmt.Errorf("got code %s, want %s", core.CodeOf(w.Secrets.lastErr), code)
		}
		return nil
	}
	return fmt.Errorf("call succeeded, want %s", code)
}

// registerShared holds phrases used by more than one domain.
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
	sc.Step(`^the put (succeeds|fails with code "[^"]+")$`, assertPutOutcome)
}

// assertPutOutcome reads the phrase the credentials and the bus scenarios share.
// The credentials store is the one under test only when a credentials scenario
// opened it; otherwise the bus KV store is.
func assertPutOutcome(ctx context.Context, tail string) error {
	w := world(ctx)
	if w.Credentials != nil && w.Credentials.store != nil {
		if tail == "succeeds" {
			if w.Credentials.putErr != nil {
				return fmt.Errorf("put failed: %w", w.Credentials.putErr)
			}
			return nil
		}
		code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
		return credentialsCode(w.Credentials.putErr, code)
	}
	return matchPutOutcome(ctx, tail)
}
