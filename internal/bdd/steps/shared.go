package steps

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

// runOutcome is what a domain world reports about the run a scenario started,
// for the sentences that flow machine and flow host scenarios share.
type runOutcome interface {
	// started reports whether this world has a run in progress or finished.
	started() bool
	// nodeRuns is how many times the node started in the run.
	nodeRuns(node string) (int, error)
	// failure is the code and message the run ended with; ok is false when it did not fail.
	failure() (code, message string, ok bool)
}

func pickRunOutcome(ctx context.Context) (runOutcome, error) {
	w := world(ctx)
	if w.Machine != nil {
		if ro, ok := any(w.Machine).(runOutcome); ok && ro.started() {
			return ro, nil
		}
	}
	if w.Host != nil {
		if ro, ok := w.Host.(runOutcome); ok && ro.started() {
			return ro, nil
		}
	}
	return nil, fmt.Errorf("no run has started")
}

func registerSharedRunOutcomeSteps(sc *godog.ScenarioContext) {
	sc.Step(`^node "([^"]+)" ran (\d+) times$`, assertNodeRuns)
	sc.Step(`^the run fails with code "([^"]+)" naming node "([^"]+)" and the (visit|cost) budget (\d+)(?: milli-points)?$`,
		assertRunFailsNamingNodeBudget)
}

func assertNodeRuns(ctx context.Context, node string, want int) error {
	ro, err := pickRunOutcome(ctx)
	if err != nil {
		return err
	}
	got, err := ro.nodeRuns(node)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("node %q ran %d times, want %d", node, got, want)
	}
	return nil
}

func assertRunFailsNamingNodeBudget(ctx context.Context, code, node, budgetKind string, budget int) error {
	ro, err := pickRunOutcome(ctx)
	if err != nil {
		return err
	}
	gotCode, message, ok := ro.failure()
	if !ok {
		return fmt.Errorf("run did not fail")
	}
	if gotCode != code {
		return fmt.Errorf("fail code %q, want %q", gotCode, code)
	}
	if !strings.Contains(message, fmt.Sprintf("%q", node)) {
		return fmt.Errorf("message %q does not name node %q", message, node)
	}
	num := strconv.Itoa(budget)
	if !strings.Contains(message, num) {
		return fmt.Errorf("message %q does not contain budget %s", message, num)
	}
	switch budgetKind {
	case "visit":
		if !strings.Contains(message, "visit budget") {
			return fmt.Errorf("message %q does not mention visit budget", message)
		}
	case "cost":
		if !strings.Contains(message, "milli-points") {
			return fmt.Errorf("message %q does not mention milli-points", message)
		}
		if !strings.Contains(message, "cost budget") {
			return fmt.Errorf("message %q does not mention cost budget", message)
		}
	}
	return nil
}
