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
	Core         *coreWorld
	Expr         *exprWorld
	SQL          *sqlWorld
	Prompt       *promptWorld
	Blob         *blobWorld
	Secrets      *secretsWorld
	Credentials  *credentialsWorld
	Schema       *schemaWorld
	Model        *modelWorld
	Catalog      *catalogWorld
	Node         *nodeWorld
	Agent        *agentWorld
	Conversation *convWorld
	Flow         *flowWorld
	Machine      *machineWorld
	Host         *hostWorld
	Local        *localWorld
	Bus          *busWorld
	Eventlog     *eventlogWorld
	Stream       *streamWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:         newCoreWorld(),
			Expr:         &exprWorld{bindings: map[string]any{}},
			SQL:          newSQLWorld(),
			Prompt:       newPromptWorld(),
			Blob:         newBlobWorld(),
			Schema:       newSchemaWorld(),
			Model:        newModelWorld(),
			Catalog:      newCatalogWorld(),
			Node:         newNodeWorld(),
			Agent:        newAgentWorld(),
			Conversation: newConvWorld(),
			Flow:         newFlowWorld(),
			Machine:      newMachineWorld(),
			Host:         newHostWorld(),
			Local:        newLocalWorld(),
			Bus:          newBusWorld(),
		}), nil
	})
	registerCore(sc)
	registerExpr(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerBlob(sc)
	registerSecrets(sc)
	registerCredentials(sc)
	registerBus(sc)
	registerSchema(sc)
	registerModel(sc)
	registerCatalog(sc)
	registerNode(sc)
	registerAgent(sc)
	registerConversation(sc)
	registerMiddleware(sc)
	registerMiddlewareCalls(sc)
	registerFlowCompile(sc)
	registerFlowMachine(sc)
	registerFlowHost(sc)
	registerLocal(sc)
	registerEventlog(sc)
	registerStreamLive(sc)
	registerSharedSteps(sc)
	registerShared(sc)
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
	return fmt.Errorf("call succeeded, want %s", code)
}

// registerShared holds phrases used by more than one domain. The SQL store and
// the Secrets port both report an open failure with the same phrase; one
// handler reads whichever domain opened in the scenario. The clock step is
// shared by the flow host and the model middleware: both worlds advance, and
// the one that is active settles.
func registerShared(sc *godog.ScenarioContext) {
	sc.Step(`^the put (succeeds|fails with code "[^"]+")$`, assertPutOutcome)
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

// assertPutOutcome reads the phrase the credentials and the bus scenarios share.
// The credentials store is the one under test only when a credentials scenario
// opened it; otherwise the bus KV store is.
func assertPutOutcome(ctx context.Context, tail string) error {
	w := world(ctx)
	if w.Credentials != nil {
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
