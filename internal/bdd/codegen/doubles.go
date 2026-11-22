package codegen

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	preset "github.com/epzero/basamento/presets/codegen"
)

// ScriptedModel answers the preset's model requests with the scenario's
// scripted plan, workspaces and findings. It records every request with its
// agent, module and prompt version.
type ScriptedModel struct {
	mu sync.Mutex

	Plan       preset.Plan
	Workspaces map[string]preset.Workspace
	Repairs    []preset.Workspace
	Reviews    [][]preset.Finding
	RepeatLast bool // reviews repeat the last scripted findings

	requests    []preset.Request
	repairIdx   int
	reviewIdx   int
	lastWS      preset.Workspace
	lastByStage map[preset.Stage]preset.Workspace
}

// NewScriptedModel returns an empty scripted model.
func NewScriptedModel() *ScriptedModel {
	return &ScriptedModel{
		Workspaces:  map[string]preset.Workspace{},
		lastByStage: map[preset.Stage]preset.Workspace{},
	}
}

// SetPlan sets the plan the planner returns and names each module's workspace
// ws-<module>. The plan's tests are the repository's default `go test ./...`.
func (m *ScriptedModel) SetPlan(modules ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Plan = preset.Plan{Tests: []string{"go test ./..."}}
	for _, name := range modules {
		m.Plan.Modules = append(m.Plan.Modules, preset.Module{Name: name})
		m.Workspaces[name] = preset.Workspace{ID: "ws-" + name}
	}
}

// SetRepairs sets the workspaces the repair stage returns, in order.
func (m *ScriptedModel) SetRepairs(ids ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Repairs = nil
	for _, id := range ids {
		m.Repairs = append(m.Repairs, preset.Workspace{ID: id})
	}
}

// SetReviews sets the findings the reviewer returns, in order.
func (m *ScriptedModel) SetReviews(reviews [][]preset.Finding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Reviews = reviews
	m.reviewIdx = 0
}

// RepeatReviews makes the last scripted finding list repeat forever.
func (m *ScriptedModel) RepeatReviews(repeat bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RepeatLast = repeat
}

// Complete answers one request.
func (m *ScriptedModel) Complete(_ context.Context, req preset.Request) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	switch req.Agent {
	case "planner":
		return json.Marshal(m.Plan)
	case "implement_module":
		ws, ok := m.Workspaces[req.Module]
		if !ok {
			ws = preset.Workspace{ID: "ws-" + req.Module}
		}
		m.lastWS = ws
		m.lastByStage[req.Stage] = ws
		return json.Marshal(ws)
	case "repair":
		ws := preset.Workspace{ID: fmt.Sprintf("ws-repair-%d", m.repairIdx+1)}
		if m.repairIdx < len(m.Repairs) {
			ws = m.Repairs[m.repairIdx]
		}
		m.repairIdx++
		m.lastWS = ws
		m.lastByStage[req.Stage] = ws
		return json.Marshal(ws)
	case "reviewer":
		var findings []preset.Finding
		if m.reviewIdx < len(m.Reviews) {
			findings = m.Reviews[m.reviewIdx]
			if m.reviewIdx < len(m.Reviews)-1 || !m.RepeatLast {
				m.reviewIdx++
			}
		}
		return json.Marshal(preset.Findings{Findings: append([]preset.Finding{}, findings...)})
	}
	return nil, fmt.Errorf("scripted model: unknown agent %q", req.Agent)
}

// Requests returns the requests recorded so far, in order.
func (m *ScriptedModel) Requests() []preset.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]preset.Request(nil), m.requests...)
}

// LastWorkspace is the workspace the most recent implement or repair returned.
func (m *ScriptedModel) LastWorkspace() preset.Workspace {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastWS
}

// LastWorkspaceFor is the workspace the model returned in stage. It lets a
// scenario compare the sandbox's input with what the model actually returned,
// not with the runner's own copy of it.
func (m *ScriptedModel) LastWorkspaceFor(stage preset.Stage) (preset.Workspace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ws, ok := m.lastByStage[stage]
	return ws, ok
}

// SandboxCall is one command the simulated sandbox ran and the result it
// returned, so a scenario can compare a later request with the verification
// that produced it.
type SandboxCall struct {
	Workspace preset.Workspace
	Command   string
	Result    preset.TestResult
}

// SimulatedSandbox owns the scripted test results. It records every call with
// the workspace and command it received.
type SimulatedSandbox struct {
	mu      sync.Mutex
	results []preset.TestResult
	calls   []SandboxCall
}

// NewSimulatedSandbox returns a sandbox whose tests pass until results are set.
func NewSimulatedSandbox() *SimulatedSandbox { return &SimulatedSandbox{} }

// SetResults sets the test results, one per verification, in order. The last
// result repeats once the script is exhausted.
func (s *SimulatedSandbox) SetResults(results ...preset.TestResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = results
}

// RunTests runs the plan's tests on workspace.
func (s *SimulatedSandbox) RunTests(_ context.Context, workspace preset.Workspace, command string) (preset.TestResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := preset.TestResult{}
	if len(s.results) > 0 {
		next = s.results[0]
		if len(s.results) > 1 {
			s.results = s.results[1:]
		}
	}
	s.calls = append(s.calls, SandboxCall{Workspace: workspace, Command: command, Result: next})
	return next, nil
}

// Calls returns the recorded calls, in order.
func (s *SimulatedSandbox) Calls() []SandboxCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SandboxCall(nil), s.calls...)
}

// Repo is one in-memory repository.
type Repo struct {
	Default string
	Heads   map[string]int
	Commits int
	Exists  bool
}

// RecordedCommit is one commit the simulated git host accepted. Branch is the
// branch it actually landed on; Default is the repository's default branch.
type RecordedCommit struct {
	Commit  preset.Commit
	Branch  string
	Default string
}

// SimulatedGitHost records commits and writer calls apart from reader state, so
// a scenario can assert "no branch", "still at its Nth commit" and "one commit".
type SimulatedGitHost struct {
	mu          sync.Mutex
	repos       map[string]*Repo
	commits     []RecordedCommit
	writerCalls int
	keys        map[string]bool
}

// NewSimulatedGitHost returns an empty git host.
func NewSimulatedGitHost() *SimulatedGitHost {
	return &SimulatedGitHost{repos: map[string]*Repo{}, keys: map[string]bool{}}
}

// SetupRepo creates repo with n commits on the default branch.
func (g *SimulatedGitHost) SetupRepo(repo, defaultBranch string, n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := &Repo{Default: defaultBranch, Heads: map[string]int{defaultBranch: n}, Commits: n, Exists: true}
	g.repos[repo] = r
}

// SetupBranch points branch at commit n of repo, without adding a commit.
func (g *SimulatedGitHost) SetupBranch(repo, branch string, n int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.repos[repo]
	if r == nil {
		return fmt.Errorf("git host: no repository %q", repo)
	}
	r.Heads[branch] = n
	return nil
}

// Commit records one write. A new repository gets a default branch named main.
func (g *SimulatedGitHost) Commit(_ context.Context, c preset.Commit) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writerCalls++
	if c.Key != "" && g.keys[c.Key] {
		return nil
	}
	if c.NewRepo {
		r := &Repo{Default: "main", Heads: map[string]int{}, Exists: true}
		r.Commits = 1
		r.Heads[r.Default] = 1
		g.repos[c.Repo] = r
		g.commits = append(g.commits, RecordedCommit{Commit: c, Branch: r.Default, Default: r.Default})
		g.markCommitted(c)
		return nil
	}
	r := g.repos[c.Repo]
	if r == nil {
		return fmt.Errorf("git host: no repository %q", c.Repo)
	}
	if c.Branch == "" {
		return fmt.Errorf("git host: %q is not a new repository, so a branch is required", c.Repo)
	}
	if _, ok := r.Heads[c.Branch]; !ok {
		from, ok := r.Heads[c.From]
		if !ok {
			return fmt.Errorf("git host: no branch %q to branch from", c.From)
		}
		r.Heads[c.Branch] = from
	}
	r.Commits++
	r.Heads[c.Branch]++
	g.commits = append(g.commits, RecordedCommit{Commit: c, Branch: c.Branch, Default: r.Default})
	g.markCommitted(c)
	return nil
}

// markCommitted records an idempotency key after the host accepted the commit.
func (g *SimulatedGitHost) markCommitted(c preset.Commit) {
	if c.Key != "" {
		g.keys[c.Key] = true
	}
}

// Commits returns the recorded commits, in order.
func (g *SimulatedGitHost) Commits() []RecordedCommit {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]RecordedCommit(nil), g.commits...)
}

// WriterCalls returns how many writes the host accepted.
func (g *SimulatedGitHost) WriterCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writerCalls
}

// Head returns the commit count of repo's branch.
func (g *SimulatedGitHost) Head(repo, branch string) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.repos[repo]
	if r == nil {
		return 0, false
	}
	n, ok := r.Heads[branch]
	return n, ok
}

// HasBranch reports whether repo has branch.
func (g *SimulatedGitHost) HasBranch(repo, branch string) bool {
	_, ok := g.Head(repo, branch)
	return ok
}
