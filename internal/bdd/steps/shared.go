// Step texts that more than one capability's scenarios use are registered once
// in this file. A capability that needs a new generic text adds it here and
// registers a source for its world; it never registers the shared text itself.
package steps

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// Shared failure phrases. A capability registers one source per phrase from
// its own register* with registerFailureSource.
const (
	callFailurePhrase    = "the call fails with code"
	openingFailurePhrase = "opening fails with code"
)

// failureCheck reports whether a capability's world holds a recorded error for
// the phrase (held), and how the assertion went (err).
type failureCheck func(ctx context.Context, code string) (held bool, err error)

type failureSource struct {
	name  string
	check failureCheck
}

// failureRegistry keeps the sources of each shared failure phrase. Sources are
// asked in registration order; the first that holds decides.
type failureRegistry struct {
	sources map[string][]failureSource
}

func newFailureRegistry() *failureRegistry {
	return &failureRegistry{sources: map[string][]failureSource{}}
}

func (r *failureRegistry) register(phrase, name string, check failureCheck) {
	r.sources[phrase] = append(r.sources[phrase], failureSource{name: name, check: check})
}

func (r *failureRegistry) assert(ctx context.Context, phrase, code string) error {
	for _, src := range r.sources[phrase] {
		held, err := src.check(ctx, code)
		if held {
			return err
		}
		if err != nil {
			return fmt.Errorf("%s: %w", src.name, err)
		}
	}
	switch phrase {
	case callFailurePhrase:
		return fmt.Errorf("call succeeded, want %s", code)
	case openingFailurePhrase:
		return fmt.Errorf("opening succeeded, want %s", code)
	default:
		return fmt.Errorf("no failure source for %q", phrase)
	}
}

// sharedFailures is reset by Initialize so every godog suite starts clean.
var sharedFailures = newFailureRegistry()

// registerFailureSource adds a capability's check for a shared failure phrase.
// held says the world has a recorded error for the phrase; err is the
// assertion result.
func registerFailureSource(phrase, name string, check failureCheck) {
	sharedFailures.register(phrase, name, check)
}

// timeSink lets shared "N pass" steps advance scenario time on a domain fake.
type timeSink interface {
	timePassed(context.Context, time.Duration) error
}

// runStarter is how a domain world starts a run with empty input from the
// shared sentence "a run of the flow has started".
type runStarter interface {
	declared() bool
	startDeclared() error
}

func pickRunStarter(ctx context.Context) (runStarter, error) {
	w := world(ctx)
	var hostRS, machineRS runStarter
	hostOK, machineOK := false, false
	if w.Host != nil {
		if rs, ok := any(w.Host).(runStarter); ok && rs.declared() {
			hostRS, hostOK = rs, true
		}
	}
	if w.Machine != nil {
		if rs, ok := any(w.Machine).(runStarter); ok && rs.declared() {
			machineRS, machineOK = rs, true
		}
	}
	if hostOK && machineOK {
		return nil, fmt.Errorf("both host and machine worlds have a declared flow")
	}
	if hostOK {
		return hostRS, nil
	}
	if machineOK {
		return machineRS, nil
	}
	return nil, fmt.Errorf("no flow declared")
}

func registerSharedRunStartStep(sc *godog.ScenarioContext) {
	sc.Step(`^a run of the flow has started$`, func(ctx context.Context) error {
		rs, err := pickRunStarter(ctx)
		if err != nil {
			return err
		}
		return rs.startDeclared()
	})
}

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
		if ro, ok := any(w.Host).(runOutcome); ok && ro.started() {
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

func registerSharedResolutionFailStep(sc *godog.ScenarioContext) {
	sc.Step(`^the resolution fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx)
		if w.Prompt != nil && w.Prompt.lastErr != nil {
			return expectCode(w.Prompt.lastErr, code)
		}
		if w.Local != nil {
			return localLastError(w.Local, code)
		}
		return fmt.Errorf("no resolution error to assert")
	})
}

// durationPassStepPattern is the shared "N … pass" step. It is anchored on a
// duration phrase parseElapsed accepts so godog's single step namespace is not
// stolen from capabilities that own sentences ending in "pass" (for example a
// runtime run start or a worker grace period). Models middleware scenarios say
// "1 more second passes"; advanceClockByDurationPhrase strips the word "more".
const durationPassStepPattern = `^(-?\d+ (?:more )?[a-z]+(?: \d+ [a-z]+)*) pass(?:es)?$`

// registerSharedSteps registers every step text more than one capability
// writes, including the two failure phrases routed through sharedFailures.
func registerSharedSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the call fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return sharedFailures.assert(ctx, callFailurePhrase, code)
	})
	sc.Step(`^opening fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return sharedFailures.assert(ctx, openingFailurePhrase, code)
	})
	sc.Step(durationPassStepPattern, advanceClockByDurationPhrase)
	sc.Step(`^the put (succeeds|fails with code "[^"]+")$`, matchPutOutcomeShared)
	registerSharedRunStartStep(sc)
	registerSharedRunOutcomeSteps(sc)
	registerSharedResolutionFailStep(sc)
}

// matchPutOutcomeShared owns the put phrase for every capability, because godog
// has one step namespace and both the bus and the credentials scenarios say
// "the put fails with code ...". The KV world is created only for bus scenarios
// (registerBus) and the credentials world only for credentials ones, so the
// worlds tell the two apart without reading the text.
func matchPutOutcomeShared(ctx context.Context, tail string) error {
	if world(ctx).Bus != nil {
		return matchPutOutcome(ctx, tail)
	}
	if strings.HasPrefix(tail, `fails with code "`) {
		code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
		return credentialsCode(credentialsW(ctx).putErr, code)
	}
	return fmt.Errorf("unknown put outcome %q", tail)
}

func advanceClockByDurationPhrase(ctx context.Context, elapsed string) error {
	// The middleware scenarios say "1 more second passes" to step the call's
	// virtual runtime; the word carries no duration of its own.
	elapsed = strings.Replace(elapsed, " more ", " ", 1)
	d, err := parseElapsed(elapsed)
	if err != nil {
		return err
	}
	w := world(ctx)
	if cw := w.Model.call; cw != nil {
		// The call runs on its own virtual runtime over the scenario clock, so
		// advancing it also fires the call's timers and wakes its goroutine.
		cw.rt.Advance(d)
	} else {
		w.Core.clock.Advance(d)
	}
	if local := w.Local; local != nil {
		if err := local.timePassed(ctx, d); err != nil {
			return err
		}
	}
	// A component server times its shutdown drain from the same frozen clock;
	// advancing its timer here is what "the seconds pass" means.
	if w.Config != nil && w.Config.shutdown != nil {
		w.Config.shutdown.advance(d)
	}
	if sink, ok := any(w.Host).(timeSink); ok {
		return sink.timePassed(ctx, d)
	}
	return nil
}
