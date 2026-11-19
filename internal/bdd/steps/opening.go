package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
	"github.com/epzero/basamento/core"
)

func registerOpeningSteps(sc *godog.ScenarioContext) {
	sc.Step(`^opening fails with code "([^"]*)"$`, assertOpeningFailsWithCode)
}

func assertOpeningFailsWithCode(ctx context.Context, code string) error {
	w := world(ctx)
	if w.SQL != nil && w.SQL.openErr != nil {
		err := w.SQL.openErr
		if string(core.CodeOf(err)) != code {
			return fmt.Errorf("got code %s, want %s", core.CodeOf(err), code)
		}
		return nil
	}
	if w.Secrets != nil && w.Secrets.lastErr != nil {
		if string(core.CodeOf(w.Secrets.lastErr)) != code {
			return fmt.Errorf("code %s want %s", core.CodeOf(w.Secrets.lastErr), code)
		}
		return nil
	}
	return fmt.Errorf("opening succeeded, want %s", code)
}
