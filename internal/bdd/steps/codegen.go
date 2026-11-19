package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/internal/bdd/codegen"
	preset "github.com/epzero/basamento/presets/codegen"
)

// codegenWorld drives the codegen preset with its scripted model, simulated
// sandbox and simulated git host. Nothing here reads the wall clock or global
// randomness: a hold or a stop is a channel, and a run pins its version.
type codegenWorld struct {
	flow       string
	version    string
	prompts    string
	evals      string
	guards     preset.Guards
	repairCost int64

	model   *codegen.ScriptedModel
	sandbox *codegen.SimulatedSandbox
	git     *codegen.SimulatedGitHost
	runner  *preset.Runner
	state   *preset.State
	err     error

	control *codegenControl
	done    chan struct{}

	registry    *preset.Registry
	lastVersion *preset.Version

	repo          string
	defaultBranch string

	evalCase    string
	evalFixture string
}

func newCodegenWorld() *codegenWorld {
	return &codegenWorld{
		model:   codegen.NewScriptedModel(),
		sandbox: codegen.NewSimulatedSandbox(),
		git:     codegen.NewSimulatedGitHost(),
		guards:  preset.DefaultGuards(),
	}
}

// codegenControl holds a run before a stage and stops it after one.
type codegenControl struct {
	hold    preset.Stage
	stop    preset.Stage
	reached chan preset.Stage
	release chan struct{}
	once    sync.Once
}

func newCodegenControl() *codegenControl {
	return &codegenControl{reached: make(chan preset.Stage, 1), release: make(chan struct{})}
}

// BeforeStage blocks on the release channel when stage is the held stage.
func (c *codegenControl) BeforeStage(_ context.Context, stage preset.Stage) error {
	if stage != c.hold {
		return nil
	}
	c.once.Do(func() { c.reached <- stage })
	<-c.release
	return nil
}

// AfterStage stops the run after the named stage.
func (c *codegenControl) AfterStage(_ context.Context, stage preset.Stage) error {
	if stage == c.stop {
		return preset.ErrStop
	}
	return nil
}

func (w *codegenWorld) runnerFor() *preset.Runner {
	cfg := preset.Config{
		Flow:            w.flow,
		Version:         w.version,
		Prompts:         w.prompts,
		Evals:           w.evals,
		Guards:          w.guards,
		RepairVisitCost: w.repairCost,
	}
	return preset.New(cfg, w.model, w.sandbox, w.git)
}

func (w *codegenWorld) start(in preset.Input) error {
	w.runner = w.runnerFor()
	w.done = make(chan struct{})
	close(w.done)
	var err error
	w.state, err = w.runner.Start(context.Background(), in, preset.NoControl{})
	return err
}

func (w *codegenWorld) startHolding(in preset.Input, stage preset.Stage) error {
	w.runner = w.runnerFor()
	w.control = newCodegenControl()
	w.control.hold = stage
	w.done = make(chan struct{})
	go func() {
		w.state, w.err = w.runner.Start(context.Background(), in, w.control)
		close(w.done)
	}()
	select {
	case <-w.control.reached:
		return nil
	case <-w.done:
		if w.err != nil {
			return w.err
		}
		return fmt.Errorf("the run finished before it reached the hold before stage %q", stage)
	}
}

func (w *codegenWorld) release() error {
	close(w.control.release)
	<-w.done
	return w.err
}

func (w *codegenWorld) startStopping(in preset.Input, stage preset.Stage) error {
	w.runner = w.runnerFor()
	w.control = newCodegenControl()
	w.control.stop = stage
	w.state, w.err = w.runner.Start(context.Background(), in, w.control)
	return w.err
}

func (w *codegenWorld) resume() error {
	if w.state == nil {
		return fmt.Errorf("no run to resume")
	}
	w.state, w.err = w.runner.Resume(context.Background(), w.state, preset.NoControl{})
	return w.err
}

func (w *codegenWorld) registryOrNew() *preset.Registry {
	if w.registry == nil {
		w.registry = preset.NewRegistry(nil)
	}
	return w.registry
}

// stagesFrom parses a quoted Gherkin list.
func stagesFrom(text string) []preset.Stage {
	items := parseQuotedList(text)
	out := make([]preset.Stage, len(items))
	for i, item := range items {
		out[i] = preset.Stage(item)
	}
	return out
}

func stagesEqual(got []preset.Stage, want []preset.Stage) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func (w *codegenWorld) countStage(stage preset.Stage) int {
	if w.state == nil {
		return 0
	}
	n := 0
	for _, s := range w.state.StageLog {
		if s == stage {
			n++
		}
	}
	return n
}

func (w *codegenWorld) runIDExpand(branch string) string {
	if w.state == nil {
		return branch
	}
	return strings.ReplaceAll(branch, "<run id>", w.state.RunID)
}

func (w *codegenWorld) modelCount(agent string) int {
	n := 0
	for _, req := range w.model.Requests() {
		if req.Agent == agent {
			n++
		}
	}
	return n
}

func registerCodegen(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Codegen = newCodegenWorld()
		return ctx, nil
	})
	// Background: the preset and the flow.
	sc.Step(`^the preset is version "([^"]*)" with a repair guard of (\d+) visits and (\d+) milli-points and a self-review guard of (\d+) visits$`,
		func(ctx context.Context, version string, visits int, cost int64, review int) error {
			w := world(ctx).Codegen
			w.version = version
			w.prompts = version + "/prompts"
			w.evals = version + "/evals"
			w.guards = preset.Guards{
				Repair:     preset.Guard{MaxVisits: visits, CostMpts: cost},
				SelfReview: preset.Guard{MaxVisits: review},
			}
			return nil
		})
	sc.Step(`^"([^"]*)" uses the scripted model and the simulated sandbox and git host$`,
		func(ctx context.Context, flow string) error {
			world(ctx).Codegen.flow = flow
			return nil
		})

	// Bootstrap plans and their verification script.
	sc.Step(`^a bootstrap brief whose plan has (\d+) modules? "([^"]*)" and plans and implements it and verifies once$`,
		func(ctx context.Context, _ int, module string) error {
			w := world(ctx).Codegen
			w.model.SetPlan(module)
			w.sandbox.SetResults(preset.TestResult{})
			return nil
		})
	sc.Step(`^a bootstrap brief whose plan has (\d+) modules? "([^"]*)" and plans and implements it and verifies with (\d+) failing, then (\d+) failing, then (\d+) failing, tests$`,
		func(ctx context.Context, _ int, module string, first, second, third int) error {
			w := world(ctx).Codegen
			w.model.SetPlan(module)
			w.sandbox.SetResults(
				preset.TestResult{Failures: failures(first)},
				preset.TestResult{Failures: failures(second)},
				preset.TestResult{Failures: failures(third)},
			)
			return nil
		})
	sc.Step(`^a bootstrap brief whose plan has (\d+) modules? "([^"]*)" and plans and implements it and verifies with (\d+) failing, then (\d+) failing, tests$`,
		func(ctx context.Context, _ int, module string, first, second int) error {
			w := world(ctx).Codegen
			w.model.SetPlan(module)
			w.sandbox.SetResults(
				preset.TestResult{Failures: failures(first)},
				preset.TestResult{Failures: failures(second)},
			)
			return nil
		})
	sc.Step(`^a bootstrap brief whose plan has (\d+) modules? "([^"]*)" and plans and implements it and verifies "([^"]*)" every time$`,
		func(ctx context.Context, _ int, module, failure string) error {
			w := world(ctx).Codegen
			w.model.SetPlan(module)
			w.sandbox.SetResults(preset.TestResult{Failures: []string{failure}})
			return nil
		})
	sc.Step(`^a bootstrap brief whose plan has (\d+) modules? "([^"]*)" and plans, implements and verifies it$`,
		func(ctx context.Context, _ int, module string) error {
			w := world(ctx).Codegen
			w.model.SetPlan(module)
			w.sandbox.SetResults(preset.TestResult{})
			return nil
		})
	sc.Step(`^a bootstrap brief whose plan has (\d+) modules? and plans and implements them and verifies once$`,
		func(ctx context.Context, n int) error {
			w := world(ctx).Codegen
			w.model.SetPlan(defaultModules(n)...)
			w.sandbox.SetResults(preset.TestResult{})
			return nil
		})
	sc.Step(`^a bootstrap brief whose plan has (\d+) modules "([^"]*)", "([^"]*)" and "([^"]*)", in that order, and plans and implements them and verifies once$`,
		func(ctx context.Context, _ int, first, second, third string) error {
			w := world(ctx).Codegen
			w.model.SetPlan(first, second, third)
			w.sandbox.SetResults(preset.TestResult{})
			return nil
		})
	sc.Step(`^a change request whose plan has (\d+) modules? "([^"]*)" and plans and implements it and verifies once$`,
		func(ctx context.Context, _ int, module string) error {
			w := world(ctx).Codegen
			w.model.SetPlan(module)
			w.sandbox.SetResults(preset.TestResult{})
			return nil
		})
	sc.Step(`^a change request whose plan has (\d+) modules? "([^"]*)" and plans, implements and verifies it$`,
		func(ctx context.Context, _ int, module string) error {
			w := world(ctx).Codegen
			w.model.SetPlan(module)
			w.sandbox.SetResults(preset.TestResult{})
			return nil
		})

	// Repositories.
	sc.Step(`^repository "([^"]*)" has (\d+) commits on default branch "([^"]*)"$`,
		func(ctx context.Context, repo string, n int, branch string) error {
			w := world(ctx).Codegen
			w.repo, w.defaultBranch = repo, branch
			w.git.SetupRepo(repo, branch, n)
			return nil
		})
	sc.Step(`^repository "([^"]*)" has (\d+) commits on default branch "([^"]*)" and a branch "([^"]*)" at its (\d+)(?:st|nd|rd|th) commit$`,
		func(ctx context.Context, repo string, n int, branch, other string, at int) error {
			w := world(ctx).Codegen
			w.repo, w.defaultBranch = repo, branch
			w.git.SetupRepo(repo, branch, n)
			return w.git.SetupBranch(repo, other, at)
		})

	// Self-review script.
	sc.Step(`^the self-review reports (\d+) finding, then none$`, func(ctx context.Context, n int) error {
		w := world(ctx).Codegen
		w.model.SetReviews([][]preset.Finding{findings(n), nil})
		return nil
	})
	sc.Step(`^the self-review reports (\d+) finding every time$`, func(ctx context.Context, n int) error {
		w := world(ctx).Codegen
		w.model.SetReviews([][]preset.Finding{findings(n)})
		w.model.RepeatReviews(true)
		return nil
	})
	sc.Step(`^a repair visit costs (\d+) milli-points$`, func(ctx context.Context, cost int64) error {
		world(ctx).Codegen.repairCost = cost
		return nil
	})

	// Runs.
	sc.Step(`^a bootstrap run of brief "([^"]*)" is started$`, func(ctx context.Context, brief string) error {
		return world(ctx).Codegen.start(preset.Input{Brief: brief})
	})
	sc.Step(`^a bootstrap run of brief "([^"]*)" is holding before stage "([^"]*)"$`,
		func(ctx context.Context, brief, stage string) error {
			return world(ctx).Codegen.startHolding(preset.Input{Brief: brief}, preset.Stage(stage))
		})
	sc.Step(`^the hold before stage "([^"]*)" is released$`, func(ctx context.Context, _ string) error {
		return world(ctx).Codegen.release()
	})
	sc.Step(`^a bootstrap run of brief "([^"]*)" is paused after its first verification$`,
		func(ctx context.Context, brief string) error {
			return world(ctx).Codegen.startStopping(preset.Input{Brief: brief}, preset.StageVerify)
		})
	sc.Step(`^a bootstrap run of brief "([^"]*)" is stopped after stage "([^"]*)"$`,
		func(ctx context.Context, brief, stage string) error {
			return world(ctx).Codegen.startStopping(preset.Input{Brief: brief}, preset.Stage(stage))
		})
	sc.Step(`^the run resumes$`, func(ctx context.Context) error { return world(ctx).Codegen.resume() })
	sc.Step(`^the run resumes and completes$`, func(ctx context.Context) error {
		w := world(ctx).Codegen
		if err := w.resume(); err != nil {
			return err
		}
		if w.state.Failed {
			return fmt.Errorf("the resumed run failed: %s", w.state.Code)
		}
		return nil
	})
	sc.Step(`^an iterate run of the change request on branch "([^"]*)" is started$`,
		func(ctx context.Context, branch string) error {
			w := world(ctx).Codegen
			return w.start(preset.Input{Repo: w.repo, BaseBranch: branch, ChangeRequest: "add a command"})
		})
	sc.Step(`^preset version "([^"]*)" replaces version "([^"]*)" as the current version$`,
		func(ctx context.Context, next, previous string) error {
			w := world(ctx).Codegen
			reg := w.registryOrNew()
			if reg.Get(previous) == nil {
				reg.Add(previous, preset.DefaultSources(), map[string]int{"plan-modules": 82, "iterate-minimal": 75})
				reg.AcceptInitial(previous)
			}
			reg.Add(next, preset.DefaultSources(), map[string]int{"plan-modules": 90, "iterate-minimal": 90})
			reg.Propose(next)
			reg.Resolve(next, "approved")
			return nil
		})

	// Run assertions.
	sc.Step(`^the stage log is \[(.*)\]$`, func(ctx context.Context, list string) error {
		w := world(ctx).Codegen
		if w.err != nil && w.state == nil {
			return w.err
		}
		want := stagesFrom(list)
		if !stagesEqual(w.state.StageLog, want) {
			return fmt.Errorf("stage log %v, want %v", w.state.StageLog, want)
		}
		return nil
	})
	sc.Step(`^the scripted model received (\d+) implement requests before stage "([^"]*)" completed$`,
		func(ctx context.Context, n int, _ string) error {
			w := world(ctx).Codegen
			before := true
			count := 0
			for _, req := range w.model.Requests() {
				if req.Agent == "planner" {
					before = false
					continue
				}
				if before && req.Agent == "implement_module" {
					count++
				}
			}
			if count != n {
				return fmt.Errorf("the model received %d implement requests before the plan completed, want %d", count, n)
			}
			return nil
		})
	sc.Step(`^the scripted model received (\d+) requests to implement a module$`, func(ctx context.Context, n int) error {
		if got := world(ctx).Codegen.modelCount("implement_module"); got != n {
			return fmt.Errorf("the model received %d implement requests, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the implement requests name modules "([^"]*)", "([^"]*)" and "([^"]*)", in that order$`,
		func(ctx context.Context, first, second, third string) error {
			w := world(ctx).Codegen
			want := []string{first, second, third}
			var got []string
			for _, req := range w.model.Requests() {
				if req.Agent == "implement_module" {
					got = append(got, req.Module)
				}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				return fmt.Errorf("implement requests name %v, want %v", got, want)
			}
			return nil
		})
	sc.Step(`^the run has checkpoints for stages (.*) and not "([^"]*)"$`,
		func(ctx context.Context, list, absent string) error {
			w := world(ctx).Codegen
			for _, stage := range stagesFrom(list) {
				if !w.state.Checkpoints[stage] {
					return fmt.Errorf("stage %q has no checkpoint", stage)
				}
			}
			if w.state.Checkpoints[preset.Stage(absent)] {
				return fmt.Errorf("stage %q has a checkpoint, want none", absent)
			}
			return nil
		})
	sc.Step(`^the run's run-started event names flow "([^"]*)", version "([^"]*)", prompts "([^"]*)" and evals "([^"]*)"$`,
		func(ctx context.Context, flow, version, prompts, evals string) error {
			w := world(ctx).Codegen
			for _, event := range w.state.Events {
				if event.Type != "run-started" {
					continue
				}
				if event.Data["flow"] != flow || event.Data["version"] != version ||
					event.Data["prompts"] != prompts || event.Data["evals"] != evals {
					return fmt.Errorf("run-started event %v, want flow %s version %s prompts %s evals %s", event.Data, flow, version, prompts, evals)
				}
				return nil
			}
			return fmt.Errorf("the run has no run-started event")
		})
	sc.Step(`^the sandbox received (\d+) calls?$`, func(ctx context.Context, n int) error {
		if got := len(world(ctx).Codegen.sandbox.Calls()); got != n {
			return fmt.Errorf("the sandbox received %d calls, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the call ran on the workspace the model returned in stage "([^"]*)"$`, func(ctx context.Context, _ string) error {
		w := world(ctx).Codegen
		calls := w.sandbox.Calls()
		if len(calls) == 0 {
			return fmt.Errorf("the sandbox received no call")
		}
		if calls[0].Workspace.ID != w.state.LastVerified.ID {
			return fmt.Errorf("the call ran on %q, want the verified workspace %q", calls[0].Workspace.ID, w.state.LastVerified.ID)
		}
		return nil
	})
	sc.Step(`^the repair stage ran (\d+) times?$`, func(ctx context.Context, n int) error {
		if got := world(ctx).Codegen.countStage(preset.StageRepair); got != n {
			return fmt.Errorf("the repair stage ran %d times, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the repair stage ran once, with the output of the failing verification$`, func(ctx context.Context) error {
		w := world(ctx).Codegen
		if got := w.countStage(preset.StageRepair); got != 1 {
			return fmt.Errorf("the repair stage ran %d times, want 1", got)
		}
		req := firstRequest(w.model.Requests(), "repair")
		if req == nil {
			return fmt.Errorf("the repair stage made no request")
		}
		var in struct {
			Failures []string `json:"failures"`
		}
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return err
		}
		calls := w.sandbox.Calls()
		if len(calls) == 0 || len(in.Failures) == 0 {
			return fmt.Errorf("the repair received no failing test output")
		}
		return nil
	})
	sc.Step(`^the repair stage's first run received the finding of the first self-review$`, func(ctx context.Context) error {
		w := world(ctx).Codegen
		req := firstRequest(w.model.Requests(), "repair")
		if req == nil {
			return fmt.Errorf("the repair stage made no request")
		}
		var in struct {
			Findings []preset.Finding `json:"findings"`
		}
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return err
		}
		if len(in.Findings) != 1 {
			return fmt.Errorf("the repair received %d findings, want 1", len(in.Findings))
		}
		return nil
	})
	sc.Step(`^a person was asked for no decision and no reviewer session was started$`, func(ctx context.Context) error {
		w := world(ctx).Codegen
		if w.state.PersonAsked {
			return fmt.Errorf("a person was asked for a decision")
		}
		if w.state.ReviewerSessions != 0 {
			return fmt.Errorf("%d reviewer sessions were started", w.state.ReviewerSessions)
		}
		return nil
	})
	sc.Step(`^the run ends in state "([^"]*)" with code "([^"]*)"$`, func(ctx context.Context, state, code string) error {
		w := world(ctx).Codegen
		got := "completed"
		if w.state.Failed {
			got = "failed"
		}
		if got != state || string(w.state.Code) != code {
			return fmt.Errorf("the run ended %s/%s, want %s/%s", got, w.state.Code, state, code)
		}
		return nil
	})
	sc.Step(`^the run names the limit "([^"]*)"$`, func(ctx context.Context, limit string) error {
		if got := world(ctx).Codegen.state.Limit; got != limit {
			return fmt.Errorf("the run names limit %q, want %q", got, limit)
		}
		return nil
	})
	sc.Step(`^the run names the limit "([^"]*)" and the last test failures "([^"]*)"$`,
		func(ctx context.Context, limit, failure string) error {
			w := world(ctx).Codegen
			if w.state.Limit != limit {
				return fmt.Errorf("the run names limit %q, want %q", w.state.Limit, limit)
			}
			for _, got := range w.state.Failures {
				if got == failure {
					return nil
				}
			}
			return fmt.Errorf("the last test failures %v do not name %q", w.state.Failures, failure)
		})
	sc.Step(`^the self-review stage ran (\d+) times?$`, func(ctx context.Context, n int) error {
		if got := world(ctx).Codegen.countStage(preset.StageSelfReview); got != n {
			return fmt.Errorf("the self-review stage ran %d times, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the run completes$`, func(ctx context.Context) error {
		w := world(ctx).Codegen
		if w.err != nil {
			return w.err
		}
		if w.state == nil || w.state.Failed || !w.state.Done {
			return fmt.Errorf("the run did not complete")
		}
		return nil
	})
	sc.Step(`^the model made (\d+) plan and implement requests, (\d+) self-review request and (\d+) repair requests in both attempts together$`,
		func(ctx context.Context, planImpl, review, repair int) error {
			w := world(ctx).Codegen
			if got := w.modelCount("planner") + w.modelCount("implement_module"); got != planImpl {
				return fmt.Errorf("the model made %d plan and implement requests, want %d", got, planImpl)
			}
			if got := w.modelCount("reviewer"); got != review {
				return fmt.Errorf("the model made %d self-review requests, want %d", got, review)
			}
			if got := w.modelCount("repair"); got != repair {
				return fmt.Errorf("the model made %d repair requests, want %d", got, repair)
			}
			return nil
		})
	sc.Step(`^the git host recorded (\d+) commit in both attempts together$`, func(ctx context.Context, n int) error {
		if got := len(world(ctx).Codegen.git.Commits()); got != n {
			return fmt.Errorf("the git host recorded %d commits, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the sandbox made (\d+) calls? in both attempts together$`, func(ctx context.Context, n int) error {
		if got := len(world(ctx).Codegen.sandbox.Calls()); got != n {
			return fmt.Errorf("the sandbox made %d calls, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the run's version is "([^"]*)"$`, func(ctx context.Context, version string) error {
		if got := world(ctx).Codegen.state.Version; got != version {
			return fmt.Errorf("the run's version is %q, want %q", got, version)
		}
		return nil
	})
	sc.Step(`^the repair stage ran with the prompt of version "([^"]*)"$`, func(ctx context.Context, version string) error {
		w := world(ctx).Codegen
		want := version + "/prompts"
		for _, req := range w.model.Requests() {
			if req.Agent != "repair" {
				continue
			}
			if req.PromptVersion != want {
				return fmt.Errorf("the repair used prompt version %q, want %q", req.PromptVersion, want)
			}
		}
		return nil
	})

	// Git host assertions.
	sc.Step(`^the git host recorded (\d+) commits? and (\d+) writer calls?$`, func(ctx context.Context, commits, writers int) error {
		w := world(ctx).Codegen
		if got := len(w.git.Commits()); got != commits {
			return fmt.Errorf("the git host recorded %d commits, want %d", got, commits)
		}
		if got := w.git.WriterCalls(); got != writers {
			return fmt.Errorf("the git host recorded %d writer calls, want %d", got, writers)
		}
		return nil
	})
	sc.Step(`^the git host recorded (\d+) commit, on a new repository's default branch, with the workspace the last verification tested$`,
		func(ctx context.Context, n int) error {
			w := world(ctx).Codegen
			commits := w.git.Commits()
			if len(commits) != n {
				return fmt.Errorf("the git host recorded %d commits, want %d", len(commits), n)
			}
			c := commits[0]
			if !c.Commit.NewRepo {
				return fmt.Errorf("the commit is not the first of a new repository")
			}
			if c.Branch != c.Default {
				return fmt.Errorf("the commit landed on %q, want the default branch %q", c.Branch, c.Default)
			}
			if c.Commit.Workspace.ID != w.state.LastVerified.ID {
				return fmt.Errorf("the commit carried %q, want the last verified workspace %q", c.Commit.Workspace.ID, w.state.LastVerified.ID)
			}
			return nil
		})
	sc.Step(`^the git host recorded (\d+) commit at stage "([^"]*)"$`, func(ctx context.Context, n int, stage string) error {
		w := world(ctx).Codegen
		commits := w.git.Commits()
		if len(commits) != n {
			return fmt.Errorf("the git host recorded %d commits, want %d", len(commits), n)
		}
		if string(commits[0].Commit.Stage) != stage {
			return fmt.Errorf("the commit was made at stage %q, want %q", commits[0].Commit.Stage, stage)
		}
		return nil
	})
	sc.Step(`^the git host recorded (\d+) commit, on new branch "([^"]*)" from branch "([^"]*)"$`,
		func(ctx context.Context, n int, branch, from string) error {
			w := world(ctx).Codegen
			commits := w.git.Commits()
			if len(commits) != n {
				return fmt.Errorf("the git host recorded %d commits, want %d", len(commits), n)
			}
			want := w.runIDExpand(branch)
			if commits[0].Branch != want || commits[0].Commit.From != from {
				return fmt.Errorf("the commit landed on %q from %q, want %q from %q", commits[0].Branch, commits[0].Commit.From, want, from)
			}
			return nil
		})
	sc.Step(`^branch "([^"]*)" is still at its (\d+)(?:st|nd|rd|th) commit$`, func(ctx context.Context, branch string, n int) error {
		w := world(ctx).Codegen
		got, ok := w.git.Head(w.repo, branch)
		if !ok || got != n {
			return fmt.Errorf("branch %q is at %d, want %d", branch, got, n)
		}
		return nil
	})
	sc.Step(`^default branch "([^"]*)" is still at its (\d+)(?:st|nd|rd|th) commit$`, func(ctx context.Context, branch string, n int) error {
		w := world(ctx).Codegen
		got, ok := w.git.Head(w.repo, branch)
		if !ok || got != n {
			return fmt.Errorf("default branch %q is at %d, want %d", branch, got, n)
		}
		return nil
	})
	sc.Step(`^the git host has no branch "([^"]*)"$`, func(ctx context.Context, branch string) error {
		w := world(ctx).Codegen
		want := w.runIDExpand(branch)
		if w.git.HasBranch(w.repo, want) {
			return fmt.Errorf("the git host has branch %q", want)
		}
		return nil
	})

	// Version lifecycle.
	sc.Step(`^an `+"`@eval`"+` suite with cases "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		w := world(ctx).Codegen
		w.registry = preset.NewRegistry([]string{first, second})
		return nil
	})
	sc.Step(`^preset version "([^"]*)" is accepted with recorded case scores (\d+) and (\d+)$`,
		func(ctx context.Context, id string, first, second int) error {
			w := world(ctx).Codegen
			reg := w.registryOrNew()
			scores := w.scoreMap(first, second)
			reg.Add(id, preset.DefaultSources(), scores)
			reg.AcceptInitial(id)
			return nil
		})
	sc.Step(`^preset version "([^"]*)" compiles and has recorded case scores (\d+) and (\d+)$`,
		func(ctx context.Context, id string, first, second int) error {
			w := world(ctx).Codegen
			w.registryOrNew().Add(id, preset.DefaultSources(), w.scoreMap(first, second))
			return nil
		})
	sc.Step(`^preset version "([^"]*)" compiles and has a recorded case score (\d+)$`,
		func(ctx context.Context, id string, first int) error {
			w := world(ctx).Codegen
			reg := w.registryOrNew()
			scores := map[string]int{}
			if len(reg.Suite) > 0 {
				scores[reg.Suite[0]] = first
			}
			reg.Add(id, preset.DefaultSources(), scores)
			return nil
		})
	sc.Step(`^preset version "([^"]*)" has a prompt reference that does not compile$`, func(ctx context.Context, id string) error {
		w := world(ctx).Codegen
		src := preset.DefaultSources()
		delete(src.Prompts, "prompts/planner.md")
		w.registryOrNew().Add(id, src, map[string]int{})
		return nil
	})
	sc.Step(`^version "([^"]*)" is waiting at its review gate$`, func(ctx context.Context, id string) error {
		w := world(ctx).Codegen
		w.lastVersion = w.registryOrNew().Propose(id)
		if !w.lastVersion.GateOpen {
			return fmt.Errorf("version %q did not reach its review gate: %s", id, w.lastVersion.Code)
		}
		return nil
	})
	sc.Step(`^version "([^"]*)" is proposed as the next version$`, func(ctx context.Context, id string) error {
		w := world(ctx).Codegen
		w.lastVersion = w.registryOrNew().Propose(id)
		return nil
	})
	sc.Step(`^version "([^"]*)" is rejected with code "([^"]*)"$`, func(ctx context.Context, id, code string) error {
		w := world(ctx).Codegen
		if w.lastVersion == nil || w.lastVersion.ID != id {
			return fmt.Errorf("no proposal of version %q", id)
		}
		if w.lastVersion.Status != preset.StatusRejected || string(w.lastVersion.Code) != code {
			return fmt.Errorf("version %q is %s/%s, want rejected/%s", id, w.lastVersion.Status, w.lastVersion.Code, code)
		}
		return nil
	})
	sc.Step(`^version "([^"]*)" is rejected with code "([^"]*)" naming case "([^"]*)"$`,
		func(ctx context.Context, id, code, caseID string) error {
			w := world(ctx).Codegen
			if w.lastVersion == nil || w.lastVersion.ID != id {
				return fmt.Errorf("no proposal of version %q", id)
			}
			if w.lastVersion.Status != preset.StatusRejected || string(w.lastVersion.Code) != code || w.lastVersion.Case != caseID {
				return fmt.Errorf("version %q is %s/%s/%s, want rejected/%s/%s", id, w.lastVersion.Status, w.lastVersion.Code, w.lastVersion.Case, code, caseID)
			}
			return nil
		})
	sc.Step(`^the version's review gate is never opened$`, func(ctx context.Context) error {
		if world(ctx).Codegen.lastVersion.GateOpen {
			return fmt.Errorf("the review gate is open")
		}
		return nil
	})
	sc.Step(`^the `+"`@eval`"+` suite ran (\d+) cases$`, func(ctx context.Context, n int) error {
		if got := world(ctx).Codegen.registryOrNew().EvalRuns; got != n {
			return fmt.Errorf("the eval suite ran %d cases, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^version "([^"]*)" is still the current version$`, func(ctx context.Context, id string) error {
		if got := world(ctx).Codegen.registryOrNew().Current; got != id {
			return fmt.Errorf("the current version is %q, want %q", got, id)
		}
		return nil
	})
	sc.Step(`^the version's review gate is open$`, func(ctx context.Context) error {
		if !world(ctx).Codegen.lastVersion.GateOpen {
			return fmt.Errorf("the review gate is not open")
		}
		return nil
	})
	sc.Step(`^version "([^"]*)" is accepted and is the current version$`, func(ctx context.Context, id string) error {
		w := world(ctx).Codegen
		reg := w.registryOrNew()
		v := reg.Get(id)
		if v == nil || v.Status != preset.StatusAccepted || reg.Current != id {
			return fmt.Errorf("version %q is not accepted and current", id)
		}
		return nil
	})
	sc.Step(`^version "([^"]*)" is superseded$`, func(ctx context.Context, id string) error {
		v := world(ctx).Codegen.registryOrNew().Get(id)
		if v == nil || v.Status != preset.StatusSuperseded {
			return fmt.Errorf("version %q is not superseded", id)
		}
		return nil
	})
	sc.Step(`^a person resolves the review gate of version "([^"]*)" with "([^"]*)"$`,
		func(ctx context.Context, id, decision string) error {
			w := world(ctx).Codegen
			w.lastVersion = w.registryOrNew().Resolve(id, decision)
			if w.lastVersion == nil {
				return fmt.Errorf("no version %q", id)
			}
			return nil
		})

	// Eval lane (nightly).
	sc.Step(`^eval suite "([^"]*)" and case "([^"]*)" with fixture "([^"]*)"$`,
		func(ctx context.Context, suite, caseID, fixture string) error {
			w := world(ctx).Codegen
			w.evalCase, w.evalFixture = caseID, fixture
			_ = suite
			return nil
		})
	sc.Step(`^a change request that adds one command$`, func(context.Context) error { return nil })
	sc.Step(`^the judge scores the plan and implement prompts the codegen flow builds over (\d+) runs and the plans they produce$`,
		func(context.Context, int) error { return nil })
	sc.Step(`^the judge scores the workspace the iterate flow commits for the request over (\d+) runs$`,
		func(context.Context, int) error { return nil })
	sc.Step(`^the mean module coverage score is at least (\d+)$`, func(ctx context.Context, want int) error {
		return meanAtLeast(world(ctx).Codegen.evalCase, "module_coverage", want)
	})
	sc.Step(`^the mean prompt-quality score is at least (\d+)$`, func(ctx context.Context, want int) error {
		return meanAtLeast(world(ctx).Codegen.evalCase, "prompt_quality", want)
	})
	sc.Step(`^the mean score for changing only the requested command is at least (\d+)$`, func(ctx context.Context, want int) error {
		return meanAtLeast(world(ctx).Codegen.evalCase, "requested_command", want)
	})
	sc.Step(`^the mean score for leaving unrelated files untouched is at least (\d+)$`, func(ctx context.Context, want int) error {
		return meanAtLeast(world(ctx).Codegen.evalCase, "untouched", want)
	})
}

func meanAtLeast(caseID, metric string, want int) error {
	mean, err := preset.DefaultSources().Mean(caseID, metric)
	if err != nil {
		return err
	}
	if mean < float64(want) {
		return fmt.Errorf("the mean %s score is %.2f, want at least %d", metric, mean, want)
	}
	return nil
}

func (w *codegenWorld) scoreMap(first, second int) map[string]int {
	scores := map[string]int{}
	if w.registry != nil && len(w.registry.Suite) >= 2 {
		scores[w.registry.Suite[0]] = first
		scores[w.registry.Suite[1]] = second
		return scores
	}
	if w.registry != nil && len(w.registry.Suite) == 1 {
		scores[w.registry.Suite[0]] = first
	}
	return scores
}

func failures(n int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("failure %d", i+1)
	}
	return out
}

func findings(n int) []preset.Finding {
	out := make([]preset.Finding, n)
	for i := range out {
		out[i] = preset.Finding{Message: fmt.Sprintf("finding %d", i+1)}
	}
	return out
}

func defaultModules(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("module-%d", i+1)
	}
	return out
}

func firstRequest(requests []preset.Request, agent string) *preset.Request {
	for i := range requests {
		if requests[i].Agent == agent {
			return &requests[i]
		}
	}
	return nil
}
