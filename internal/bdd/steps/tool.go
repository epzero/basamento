package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/tool"
)

// toolWorld drives the tools scenarios over the real tool layer: the call
// step, allowlists, schema validation, the MCP client and the sandbox port.
// The MCP server and the sandbox are deterministic simulators; nothing here
// dials a network or reads a real clock.
type toolWorld struct {
	tools     map[string]*toolDef
	agents    map[string]*toolAgent
	flows     map[string]*toolFlow
	mcps      map[string]*bddMCPSim
	sandboxes map[string]*bddSandbox
	clients   map[string]*tool.MCPClient
	creds     *bddCredentials
	blob      *bddBlob
	logBuf    *lockedBuffer
	secrets   []string

	offered    []string
	last       tool.Outcome
	lastErr    error
	lastTool   tool.Tool
	lastDetail map[string]any
	modelMsg   string
	runState   string
	callStart  time.Time
	callEnded  time.Time
	pending    *pendingCall

	journal     map[string]json.RawMessage
	keys        []string
	events      []string
	history     []string
	resumed     tool.Outcome
	interrupted bool
	listErr     error

	hostEnv   map[string]string
	hostFiles map[string]bool
}

type toolDef struct {
	tool.Tool
	mu        sync.Mutex
	calls     int
	result    json.RawMessage
	failCode  core.Code
	failMsg   string
	retry     bool
	details   map[string]any
	panics    bool
	size      int
	progress  bool
	runFor    time.Duration
	stoppedAt time.Time
	started   chan struct{}
	startOnce sync.Once
	stopCh    chan struct{}
	stopOnce  sync.Once
	sandbox   string
	owner     string
	mcpServer string
}

type toolAgent struct {
	tools   []string
	servers []string
}

type toolFlow struct {
	name     string
	tools    []string
	nodeTool string
}

type pendingCall struct {
	done    chan struct{}
	clock   tool.Clock
	outcome tool.Outcome
	err     error
}

func newToolWorld() *toolWorld {
	return &toolWorld{
		tools:     map[string]*toolDef{},
		agents:    map[string]*toolAgent{},
		flows:     map[string]*toolFlow{},
		mcps:      map[string]*bddMCPSim{},
		sandboxes: map[string]*bddSandbox{},
		clients:   map[string]*tool.MCPClient{},
		creds:     &bddCredentials{values: map[string][]byte{}},
		blob:      &bddBlob{data: map[string][]byte{}},
		logBuf:    &lockedBuffer{},
		journal:   map[string]json.RawMessage{},
		hostEnv:   map[string]string{},
		hostFiles: map[string]bool{},
	}
}

func (w *toolWorld) teardown() {
	for _, client := range w.clients {
		_ = client.Close()
	}
	w.clients = map[string]*tool.MCPClient{}
}

func (w *toolWorld) layer(wr *World) *tool.Layer {
	return tool.New(tool.Config{
		Clock:       wr.Core.clock,
		Blob:        w.blob,
		Credentials: w.creds,
		Log:         slog.New(slog.NewTextHandler(w.logBuf, nil)),
	})
}

func (w *toolWorld) ensureTool(name string) *toolDef {
	d := w.tools[name]
	if d == nil {
		d = &toolDef{Tool: tool.Tool{Name: name}, started: make(chan struct{}), stopCh: make(chan struct{})}
		w.tools[name] = d
	}
	return d
}

func (d *toolDef) inc() {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
}

func (d *toolDef) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (w *toolWorld) agent(name string) *toolAgent {
	a := w.agents[name]
	if a == nil {
		a = &toolAgent{}
		w.agents[name] = a
	}
	return a
}

func (w *toolWorld) giveTool(agent, name string) {
	d := w.ensureTool(name)
	if d.owner == "" {
		d.owner = agent
	}
	a := w.agent(agent)
	for _, existing := range a.tools {
		if existing == name {
			return
		}
	}
	a.tools = append(a.tools, name)
}

func (w *toolWorld) removeTool(agent, name string) {
	a := w.agent(agent)
	kept := a.tools[:0]
	for _, existing := range a.tools {
		if existing != name {
			kept = append(kept, existing)
		}
	}
	a.tools = kept
}

func (w *toolWorld) setFor(agent string) *tool.Set {
	return w.setFrom(w.agent(agent).tools)
}

func (w *toolWorld) setFrom(names []string) *tool.Set {
	set := &tool.Set{}
	for _, name := range names {
		d := w.tools[name]
		if d == nil {
			continue
		}
		set.Tools = append(set.Tools, w.buildTool(d))
	}
	sort.Slice(set.Tools, func(i, j int) bool { return set.Tools[i].Name < set.Tools[j].Name })
	return set
}

func (w *toolWorld) buildTool(d *toolDef) tool.Tool {
	if d.Run != nil {
		return d.Tool
	}
	t := d.Tool
	t.Run = func(ctx context.Context, args json.RawMessage) (tool.Result, error) {
		return w.runTool(ctx, d, args)
	}
	return t
}

func (w *toolWorld) runTool(ctx context.Context, d *toolDef, args json.RawMessage) (tool.Result, error) {
	d.inc()
	switch {
	case d.panics:
		panic("the tool panicked")
	case d.sandbox != "":
		return w.runSandboxTool(ctx, d, args)
	case d.progress:
		return w.runProgress(ctx, d)
	case d.size > 0:
		return tool.Result{Content: rawString(d.size)}, nil
	case d.failCode != "":
		return tool.Result{}, core.NewError(d.failCode, d.failMsg, d.details)
	case d.result != nil:
		return tool.Result{Content: d.result}, nil
	default:
		return tool.Result{Content: json.RawMessage(`"ok"`)}, nil
	}
}

// runProgress reports progress every second on the scenario clock. It stops at
// its next heartbeat after the layer cancels its context (REQ-TOOL-006).
func (w *toolWorld) runProgress(ctx context.Context, d *toolDef) (tool.Result, error) {
	clock := world(ctx).Core.clock
	d.startOnce.Do(func() { close(d.started) })
	start := clock.Now()
	for {
		<-clock.After(time.Second)
		if ctx.Err() != nil {
			d.stoppedAt = clock.Now()
			d.stopOnce.Do(func() { close(d.stopCh) })
			return tool.Result{}, ctx.Err()
		}
		if d.runFor > 0 && clock.Now().Sub(start) >= d.runFor {
			d.stopOnce.Do(func() { close(d.stopCh) })
			return tool.Result{Content: json.RawMessage(`"done"`)}, nil
		}
	}
}

func (w *toolWorld) runSandboxTool(ctx context.Context, d *toolDef, args json.RawMessage) (tool.Result, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, core.NewError(core.CodeInvalidArgument, "run-tests: invalid arguments", nil)
	}
	sb := w.sandboxes[d.sandbox]
	if sb == nil {
		return tool.Result{}, core.NewError(core.CodeInternal, "run-tests: no sandbox", nil)
	}
	return w.layer(world(ctx)).RunSandbox(ctx, sb, sb.spec(), strings.Fields(in.Command))
}

func rawString(n int) json.RawMessage {
	if n < 2 {
		n = 2
	}
	return json.RawMessage(`"` + strings.Repeat("a", n-2) + `"`)
}

func (w *toolWorld) call(ctx context.Context, agent, name string, args json.RawMessage) error {
	d := w.ensureTool(name)
	clk := world(ctx).Core.clock
	w.callStart = clk.Now()
	w.callEnded = time.Time{}
	set := w.setFor(agent)
	run := func() (tool.Outcome, error) {
		return w.layer(world(ctx)).Step(ctx, set, tool.Call{Name: name, Args: args})
	}
	if d.progress || (d.sandbox != "" && w.sandboxes[d.sandbox] != nil && w.sandboxes[d.sandbox].runUntilStopped) {
		p := &pendingCall{done: make(chan struct{}), clock: clk}
		w.pending = p
		go func() {
			p.outcome, p.err = run()
			close(p.done)
		}()
		// Do not return before the effect has started: its time limit must be
		// registered before the scenario advances the clock.
		if d.sandbox != "" {
			<-w.sandboxes[d.sandbox].startCh
		} else {
			<-d.started
		}
		return nil
	}
	out, err := run()
	w.finish(clk, out, err)
	return err
}

func (w *toolWorld) finish(clk tool.Clock, out tool.Outcome, err error) {
	w.last = out
	w.lastErr = err
	w.callEnded = clk.Now()
	if err == nil {
		w.runState = "running"
	}
	raw, _ := json.Marshal(out)
	w.events = append(w.events, "outcome "+string(raw))
}

func (w *toolWorld) await() error {
	p := w.pending
	if p == nil {
		return nil
	}
	<-p.done
	w.pending = nil
	w.finish(p.clock, p.outcome, p.err)
	return p.err
}

func (w *toolWorld) advance(wr *World, d time.Duration) error {
	wr.Core.clock.Advance(d)
	return w.await()
}

func (w *toolWorld) startTurn(ctx context.Context, agent string) {
	w.refreshMCP(ctx, agent)
	w.offered = w.setFor(agent).Names()
}

// refreshMCP re-reads each MCP server the agent uses before a model turn, so
// the offer always reflects the server's current tools (REQ-TOOL-016).
func (w *toolWorld) refreshMCP(ctx context.Context, agent string) {
	a := w.agent(agent)
	for _, server := range a.servers {
		client := w.clients[server]
		if client == nil {
			continue
		}
		existing := &tool.Set{}
		for _, name := range a.tools {
			d := w.tools[name]
			if d == nil || d.mcpServer == server {
				continue
			}
			existing.Tools = append(existing.Tools, w.buildTool(d))
		}
		tools, err := client.Tools(ctx, existing)
		if err != nil {
			w.listErr = err
			continue
		}
		kept := a.tools[:0]
		for _, name := range a.tools {
			d := w.tools[name]
			if d != nil && d.mcpServer == server {
				delete(w.tools, name)
				continue
			}
			kept = append(kept, name)
		}
		a.tools = kept
		for _, offered := range tools {
			w.tools[offered.Name] = &toolDef{Tool: offered, mcpServer: server}
			a.tools = append(a.tools, offered.Name)
		}
	}
}

// runFlowStep runs one tool node as a durable step: the journal holds the
// outcome under tool.Key, so a resume returns it without running the tool
// again (REQ-TOOL-003).
func (w *toolWorld) runFlowStep(ctx context.Context, runID, path string, visit, index int, set *tool.Set, name string) (tool.Outcome, error) {
	key := tool.Key(runID, path, visit, index)
	w.keys = append(w.keys, key)
	if raw, ok := w.journal[key]; ok {
		var out tool.Outcome
		if err := json.Unmarshal(raw, &out); err != nil {
			return tool.Outcome{}, err
		}
		return out, nil
	}
	out, err := w.layer(world(ctx)).Step(ctx, set, tool.Call{Name: name, Args: json.RawMessage(`{}`)})
	if err != nil {
		return tool.Outcome{}, err
	}
	raw, _ := json.Marshal(out)
	w.journal[key] = raw
	w.finish(world(ctx).Core.clock, out, nil)
	return out, nil
}

// --- the simulated MCP server ---------------------------------------------

type bddMCPSim struct {
	name     string
	server   *mcp.Server
	mu       sync.Mutex
	offline  bool
	drop     bool
	credRef  string
	answers  map[string]json.RawMessage
	failures map[string]string
	calls    []bddMCPCall
	creds    [][]byte
	lastLink *bddDropTransport
}

type bddMCPCall struct {
	name string
	args json.RawMessage
}

type bddDropTransport struct {
	mcp.Transport
	mu   sync.Mutex
	conn mcp.Connection
}

func (t *bddDropTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.Transport.Connect(ctx)
	if err == nil {
		t.mu.Lock()
		t.conn = conn
		t.mu.Unlock()
	}
	return conn, err
}

func (t *bddDropTransport) drop() {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (w *toolWorld) mcp(name string) *bddMCPSim {
	s := w.mcps[name]
	if s == nil {
		s = &bddMCPSim{
			name:     name,
			answers:  map[string]json.RawMessage{},
			failures: map[string]string{},
		}
		s.server = mcp.NewServer(
			&mcp.Implementation{Name: "sim-" + name, Version: "0"},
			&mcp.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		)
		w.mcps[name] = s
	}
	return s
}

func (s *bddMCPSim) addTool(name, description, required, typ string) {
	prop := map[string]any{}
	if typ != "" {
		prop["type"] = typ
	}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{required: prop},
		"required":   []string{required},
	}
	s.server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema}, s.handler(name))
}

func (s *bddMCPSim) handler(name string) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		s.mu.Lock()
		s.calls = append(s.calls, bddMCPCall{name: name, args: append(json.RawMessage(nil), req.Params.Arguments...)})
		drop := s.drop
		failure := s.failures[name]
		answer := s.answers[name]
		link := s.lastLink
		s.mu.Unlock()
		if drop {
			if link != nil {
				link.drop()
			}
			return nil, errors.New("connection dropped")
		}
		if failure != "" {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: failure}},
			}, nil
		}
		if len(answer) == 0 {
			answer = json.RawMessage(`{"ok":true}`)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(answer)}},
		}, nil
	}
}

func (s *bddMCPSim) dial(ctx context.Context, credential []byte) (mcp.Transport, error) {
	s.mu.Lock()
	offline := s.offline
	if len(credential) > 0 {
		s.creds = append(s.creds, append([]byte(nil), credential...))
	}
	s.mu.Unlock()
	if offline {
		return nil, errors.New("offline")
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	link := &bddDropTransport{Transport: clientTransport}
	s.mu.Lock()
	s.lastLink = link
	s.mu.Unlock()
	go func() { _ = s.server.Run(context.Background(), serverTransport) }()
	return link, nil
}

func (s *bddMCPSim) callCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, call := range s.calls {
		if call.name == name {
			n++
		}
	}
	return n
}

func (s *bddMCPSim) totalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *bddMCPSim) receivedCredential(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.creds {
		if string(value) == secret {
			return true
		}
	}
	return false
}

// --- the simulated sandbox -------------------------------------------------

type bddSandbox struct {
	name            string
	mu              sync.Mutex
	timeLimit       time.Duration
	maxOutput       int
	network         []string
	runUntilStopped bool
	answers         map[string]bddSandboxAnswer
	cannotStart     bool
	started         int
	gotSpec         tool.SandboxSpec
	commands        []string
	stopped         bool
	startCh         chan struct{}
	startOnce       sync.Once
	stopCh          chan struct{}
	stopOnce        sync.Once
}

type bddSandboxAnswer struct {
	exit   int
	stdout []byte
}

func (w *toolWorld) sandbox(name string) *bddSandbox {
	s := w.sandboxes[name]
	if s == nil {
		s = &bddSandbox{name: name, answers: map[string]bddSandboxAnswer{}, startCh: make(chan struct{}), stopCh: make(chan struct{})}
		w.sandboxes[name] = s
	}
	return s
}

func (s *bddSandbox) spec() tool.SandboxSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return tool.SandboxSpec{
		TimeLimit: s.timeLimit,
		MaxOutput: s.maxOutput,
		Network:   append([]string(nil), s.network...),
	}
}

func (s *bddSandbox) Run(ctx context.Context, spec tool.SandboxSpec, command []string) (tool.SandboxResult, error) {
	s.mu.Lock()
	cannotStart := s.cannotStart
	if cannotStart {
		s.mu.Unlock()
		return tool.SandboxResult{}, errors.New("the sandbox cannot start")
	}
	s.startOnce.Do(func() { close(s.startCh) })
	s.started++
	s.gotSpec = spec
	s.commands = append(s.commands, strings.Join(command, " "))
	runUntilStopped := s.runUntilStopped
	answer := s.answers[strings.Join(command, " ")]
	s.mu.Unlock()
	if runUntilStopped {
		<-ctx.Done()
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
		s.stopOnce.Do(func() { close(s.stopCh) })
		return tool.SandboxResult{}, ctx.Err()
	}
	return tool.SandboxResult{ExitStatus: answer.exit, Stdout: answer.stdout}, nil
}

func (s *bddSandbox) received() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// --- platform doubles ------------------------------------------------------

// bddBlob is an in-memory platform.Blob.
type bddBlob struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (b *bddBlob) Put(_ context.Context, key string, r io.Reader, _ string) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[key] = raw
	return nil
}

func (b *bddBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, ok := b.data[key]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no blob", nil)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (b *bddBlob) Exists(_ context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.data[key]
	return ok, nil
}

func (b *bddBlob) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data, key)
	return nil
}

func (b *bddBlob) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "bdd://" + key, nil
}

// StartUpload and Commit are not used by tool scenarios; the layer stores
// oversized results with Put. The port requires them, so they fail loudly.
func (b *bddBlob) StartUpload(context.Context, platform.UploadOptions) (platform.Upload, error) {
	return platform.Upload{}, core.NewError(core.CodeInvalidArgument, "tool scenarios do not stage uploads", nil)
}

func (b *bddBlob) Commit(context.Context, string) (string, error) {
	return "", core.NewError(core.CodeInvalidArgument, "tool scenarios do not stage uploads", nil)
}

func (b *bddBlob) size(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data[key])
}

// bddCredentials is a platform.Credentials over an in-memory vault.
type bddCredentials struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (c *bddCredentials) Put(_ context.Context, name string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values["cred://"+name] = append([]byte(nil), value...)
	return nil
}

func (c *bddCredentials) Resolve(_ context.Context, ref string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[ref]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no credential", nil)
	}
	return value, nil
}

func (c *bddCredentials) Delete(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, "cred://"+name)
	return nil
}

func (c *bddCredentials) Names(context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.values))
	for ref := range c.values {
		names = append(names, strings.TrimPrefix(ref, "cred://"))
	}
	sort.Strings(names)
	return names, nil
}

// lockedBuffer is a slog writer safe for the layer's goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// --- helpers ---------------------------------------------------------------

func requiredOf(input json.RawMessage) ([]string, error) {
	var doc struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(input, &doc); err != nil {
		return nil, fmt.Errorf("input schema is not a JSON object: %w", err)
	}
	return doc.Required, nil
}

func propertiesOf(input json.RawMessage) ([]string, error) {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(input, &doc); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(doc.Properties))
	for name := range doc.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func sameJSON(a, b []byte) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	ar, _ := json.Marshal(av)
	br, _ := json.Marshal(bv)
	return bytes.Equal(ar, br)
}

func outputText(content json.RawMessage) string {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	return string(content)
}

func toolSchema(properties map[string]any, required []string, closed bool) json.RawMessage {
	doc := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		doc["required"] = required
	}
	if closed {
		doc["additionalProperties"] = false
	}
	raw, _ := json.Marshal(doc)
	return raw
}

// --- steps -----------------------------------------------------------------

func registerTool(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w := world(ctx)
		w.Tool = newToolWorld()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Tool; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	// Tool declarations.
	sc.Step(`^a flow "([^"]*)" whose tool node calls tool "([^"]*)"$`, func(ctx context.Context, flow, name string) error {
		w := world(ctx).Tool
		w.ensureTool(name)
		w.flows[flow] = &toolFlow{name: flow, tools: []string{name}, nodeTool: name}
		return nil
	})
	sc.Step(`^a flow "([^"]*)" is given tool "([^"]*)" and its tool node calls tool "([^"]*)"$`, func(ctx context.Context, flow, allowed, called string) error {
		w := world(ctx).Tool
		w.ensureTool(allowed)
		w.ensureTool(called)
		w.flows[flow] = &toolFlow{name: flow, tools: []string{allowed}, nodeTool: called}
		return nil
	})
	sc.Step(`^tool "([^"]*)" has the description "([^"]*)" and an input schema requiring "([^"]*)"$`, func(ctx context.Context, name, description, required string) error {
		d := world(ctx).Tool.ensureTool(name)
		d.Description = description
		d.Input = toolSchema(map[string]any{required: map[string]any{}}, []string{required}, false)
		return nil
	})
	sc.Step(`^tool "([^"]*)" exists$`, func(ctx context.Context, name string) error {
		world(ctx).Tool.ensureTool(name)
		return nil
	})
	sc.Step(`^tool "([^"]*)" exists and is registered$`, func(ctx context.Context, name string) error {
		world(ctx).Tool.ensureTool(name)
		return nil
	})
	sc.Step(`^tool "([^"]*)" exists and is given to agent "([^"]*)"$`, func(ctx context.Context, name, agent string) error {
		world(ctx).Tool.giveTool(agent, name)
		return nil
	})
	sc.Step(`^the tool layer describes tool "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx).Tool
		d := w.ensureTool(name)
		w.lastTool = w.buildTool(d)
		return nil
	})
	sc.Step(`^the description is "([^"]*)"$`, func(ctx context.Context, want string) error {
		if got := world(ctx).Tool.lastTool.Description; got != want {
			return fmt.Errorf("description %q, want %q", got, want)
		}
		return nil
	})
	sc.Step(`^the input schema requires "([^"]*)"$`, func(ctx context.Context, name string) error {
		formatted, err := requiredOf(world(ctx).Tool.lastTool.Input)
		if err != nil {
			return err
		}
		if len(formatted) != 1 || formatted[0] != name {
			return fmt.Errorf("schema requires %v, want %q", formatted, name)
		}
		return nil
	})
	sc.Step(`^the schema's properties are "([^"]*)" only$`, func(ctx context.Context, name string) error {
		properties, err := propertiesOf(world(ctx).Tool.lastTool.Input)
		if err != nil {
			return err
		}
		if len(properties) != 1 || properties[0] != name {
			return fmt.Errorf("schema properties %v, want only %q", properties, name)
		}
		return nil
	})

	// Allowlists and model turns.
	sc.Step(`^agent "([^"]*)" is given tools "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, agent, first, second string) error {
		w := world(ctx).Tool
		w.giveTool(agent, first)
		w.giveTool(agent, second)
		return nil
	})
	sc.Step(`^agent "([^"]*)" is given tool "([^"]*)"$`, func(ctx context.Context, agent, name string) error {
		world(ctx).Tool.giveTool(agent, name)
		return nil
	})
	sc.Step(`^agent "([^"]*)" is given tool "([^"]*)", whose input schema requires "([^"]*)"$`, func(ctx context.Context, agent, name, required string) error {
		w := world(ctx).Tool
		w.giveTool(agent, name)
		w.tools[name].Input = toolSchema(map[string]any{required: map[string]any{}}, []string{required}, false)
		return nil
	})
	sc.Step(`^agent "([^"]*)" is given tool "([^"]*)", whose input schema requires "([^"]*)" of type "([^"]*)" and allows no other properties$`, func(ctx context.Context, agent, name, required, typ string) error {
		w := world(ctx).Tool
		w.giveTool(agent, name)
		w.tools[name].Input = toolSchema(map[string]any{required: map[string]any{"type": typ}}, []string{required}, true)
		return nil
	})
	sc.Step(`^agent "([^"]*)" is given tool "([^"]*)", whose time limit is (\d+) seconds and which reports progress every second$`, func(ctx context.Context, agent, name string, seconds int) error {
		w := world(ctx).Tool
		w.giveTool(agent, name)
		w.tools[name].Timeout = time.Duration(seconds) * time.Second
		w.tools[name].progress = true
		return nil
	})
	sc.Step(`^agent "([^"]*)" is given tool "([^"]*)", whose result size limit is (\d+) KiB$`, func(ctx context.Context, agent, name string, kib int) error {
		w := world(ctx).Tool
		w.giveTool(agent, name)
		w.tools[name].MaxResult = kib * 1024
		return nil
	})
	sc.Step(`^tool "([^"]*)" is removed from agent "([^"]*)"'s allowlist$`, func(ctx context.Context, name, agent string) error {
		world(ctx).Tool.removeTool(agent, name)
		return nil
	})
	sc.Step(`^agent "([^"]*)" starts a model turn$`, func(ctx context.Context, agent string) error {
		world(ctx).Tool.startTurn(ctx, agent)
		return nil
	})
	sc.Step(`^agent "([^"]*)" has completed (\d+) model turn offered tools "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, agent string, turns int, first, second string) error {
		world(ctx).Tool.startTurn(ctx, agent)
		return offeredExactly(world(ctx).Tool.offered, first, second)
	})
	sc.Step(`^the model is offered the tools "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		return offeredExactly(world(ctx).Tool.offered, first, second)
	})
	sc.Step(`^the model is offered the tools "([^"]*)"$`, func(ctx context.Context, only string) error {
		return offeredExactly(world(ctx).Tool.offered, only)
	})
	sc.Step(`^agent "([^"]*)" is offered the tools "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, agent, first, second string) error {
		return offeredExactly(world(ctx).Tool.offered, first, second)
	})
	sc.Step(`^the model is not offered a tool named "([^"]*)"$`, func(ctx context.Context, name string) error {
		for _, offered := range world(ctx).Tool.offered {
			if offered == name {
				return fmt.Errorf("the model is offered %q", name)
			}
		}
		return nil
	})

	// Tool behavior.
	sc.Step(`^tool "([^"]*)" succeeds with the result (\{.*\})$`, func(ctx context.Context, name, result string) error {
		world(ctx).Tool.ensureTool(name).result = json.RawMessage(result)
		return nil
	})
	sc.Step(`^tool "([^"]*)" returns the error "([^"]*)" with code "([^"]*)"$`, func(ctx context.Context, name, message, code string) error {
		d := world(ctx).Tool.ensureTool(name)
		d.failMsg, d.failCode = message, core.Code(code)
		return nil
	})
	sc.Step(`^tool "([^"]*)" returns the error "([^"]*)" with code "([^"]*)" and marks it retryable$`, func(ctx context.Context, name, message, code string) error {
		d := world(ctx).Tool.ensureTool(name)
		d.failMsg, d.failCode, d.retry = message, core.Code(code), true
		return nil
	})
	sc.Step(`^tool "([^"]*)" returns the error "([^"]*)" with code "([^"]*)" and details (\{.*\})$`, func(ctx context.Context, name, message, code, details string) error {
		d := world(ctx).Tool.ensureTool(name)
		d.failMsg, d.failCode = message, core.Code(code)
		if err := json.Unmarshal([]byte(details), &d.details); err != nil {
			return err
		}
		world(ctx).Tool.lastDetail = d.details
		return nil
	})
	sc.Step(`^tool "([^"]*)" panics when it runs$`, func(ctx context.Context, name string) error {
		world(ctx).Tool.ensureTool(name).panics = true
		return nil
	})
	sc.Step(`^tool "([^"]*)" runs for (\d+) seconds$`, func(ctx context.Context, name string, seconds int) error {
		w := world(ctx).Tool
		d := w.ensureTool(name)
		d.runFor = time.Duration(seconds) * time.Second
		if d.progress && d.owner != "" {
			return w.call(ctx, d.owner, name, json.RawMessage(`{}`))
		}
		return nil
	})
	sc.Step(`^tool "([^"]*)" returns a result of (\d+) bytes$`, func(ctx context.Context, name string, size int) error {
		world(ctx).Tool.ensureTool(name).size = size
		return nil
	})

	// Calls and assertions.
	sc.Step(`^agent "([^"]*)" calls tool "([^"]*)" with arguments (\{.*\})$`, func(ctx context.Context, agent, name, args string) error {
		return world(ctx).Tool.call(ctx, agent, name, json.RawMessage(args))
	})
	sc.Step(`^agent "([^"]*)" calls tool "([^"]*)"$`, func(ctx context.Context, agent, name string) error {
		return world(ctx).Tool.call(ctx, agent, name, json.RawMessage(`{}`))
	})
	sc.Step(`^agent "([^"]*)" calls tool "([^"]*)" and starts a model turn$`, func(ctx context.Context, agent, name string) error {
		w := world(ctx).Tool
		if err := w.call(ctx, agent, name, json.RawMessage(`{}`)); err != nil {
			return err
		}
		w.modelMsg = w.last.Text()
		w.startTurn(ctx, agent)
		return nil
	})
	sc.Step(`^tool "([^"]*)" was not run$`, func(ctx context.Context, name string) error {
		if got := world(ctx).Tool.ensureTool(name).count(); got != 0 {
			return fmt.Errorf("tool %s ran %d times, want 0", name, got)
		}
		return nil
	})
	sc.Step(`^tool "([^"]*)" was run (\d+) times?$`, func(ctx context.Context, name string, n int) error {
		if got := world(ctx).Tool.ensureTool(name).count(); got != n {
			return fmt.Errorf("tool %s ran %d times, want %d", name, got, n)
		}
		return nil
	})
	sc.Step(`^the call succeeds$`, func(ctx context.Context) error {
		w := world(ctx).Tool
		if w.lastErr != nil {
			return w.lastErr
		}
		if w.last.Failed {
			return fmt.Errorf("the call failed with %s: %s", w.last.Code, w.last.Message)
		}
		return nil
	})
	sc.Step(`^the call ends with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return callFailed(world(ctx).Tool, code, "")
	})
	sc.Step(`^the call ends with code "([^"]*)" and message "([^"]*)"$`, func(ctx context.Context, code, message string) error {
		return callFailed(world(ctx).Tool, code, message)
	})
	sc.Step(`^the call ends with code "([^"]*)" after (\d+) seconds of clock time$`, func(ctx context.Context, code string, seconds int) error {
		w := world(ctx).Tool
		if err := callFailed(w, code, ""); err != nil {
			return err
		}
		if got := w.callEnded.Sub(w.callStart); got != time.Duration(seconds)*time.Second {
			return fmt.Errorf("the call ended after %s of clock time, want %d seconds", got, seconds)
		}
		return nil
	})
	sc.Step(`^the call's message names "([^"]*)"$`, func(ctx context.Context, name string) error {
		if !strings.Contains(world(ctx).Tool.last.Message, name) {
			return fmt.Errorf("message %q does not name %q", world(ctx).Tool.last.Message, name)
		}
		return nil
	})
	sc.Step(`^the run state is "([^"]*)"$`, func(ctx context.Context, state string) error {
		if got := world(ctx).Tool.runState; got != state {
			return fmt.Errorf("run state %q, want %q", got, state)
		}
		return nil
	})
	sc.Step(`^the error names "([^"]*)" as required$`, func(ctx context.Context, name string) error {
		return errorNames(world(ctx).Tool.last.Message, name, "required")
	})
	sc.Step(`^the error names "([^"]*)" as (must be a string|not allowed)$`, func(ctx context.Context, name, requirement string) error {
		return errorNames(world(ctx).Tool.last.Message, name, requirement)
	})
	sc.Step(`^the model receives the error code "([^"]*)" and message "([^"]*)"$`, func(ctx context.Context, code, message string) error {
		w := world(ctx).Tool
		if !strings.Contains(w.modelMsg, code) || !strings.Contains(w.modelMsg, message) {
			return fmt.Errorf("the model received %q, want code %q and message %q", w.modelMsg, code, message)
		}
		w.history = append(w.history, w.modelMsg)
		return nil
	})
	sc.Step(`^the model receives the error code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).Tool
		if !strings.Contains(w.modelMsg, code) {
			return fmt.Errorf("the model received %q, want code %q", w.modelMsg, code)
		}
		w.history = append(w.history, w.modelMsg)
		return nil
	})
	sc.Step(`^the model receives no details$`, func(ctx context.Context) error {
		w := world(ctx).Tool
		for key, value := range w.lastDetail {
			text := fmt.Sprint(value)
			if strings.Contains(w.modelMsg, text) {
				return fmt.Errorf("the model received detail %s=%v: %q", key, value, w.modelMsg)
			}
		}
		return nil
	})
	sc.Step(`^the result is a blob reference and not the result's bytes$`, func(ctx context.Context) error {
		if got := world(ctx).Tool.last.Result.BlobRef; got == "" || world(ctx).Tool.last.Result.Content != nil {
			return fmt.Errorf("result %+v is not a blob reference", world(ctx).Tool.last.Result)
		}
		return nil
	})
	sc.Step(`^blob storage holds the (\d+) bytes at that reference$`, func(ctx context.Context, size int) error {
		w := world(ctx).Tool
		if got := w.blob.size(w.last.Result.BlobRef); got != size {
			return fmt.Errorf("blob holds %d bytes, want %d", got, size)
		}
		return nil
	})
	sc.Step(`^after (\d+) more seconds? tool "([^"]*)" was stopped$`, func(ctx context.Context, seconds int, name string) error {
		w := world(ctx).Tool
		world(ctx).Core.clock.Advance(time.Duration(seconds) * time.Second)
		<-w.ensureTool(name).stopCh
		return nil
	})

	// Replay and step keys.
	sc.Step(`^the run of flow "([^"]*)" ran the call and was interrupted before its next step$`, func(ctx context.Context, flow string) error {
		w := world(ctx).Tool
		f := w.flows[flow]
		if f == nil {
			return fmt.Errorf("no flow %q", flow)
		}
		_, err := w.runFlowStep(ctx, "run_"+flow, flow, 1, 1, w.setFrom(f.tools), f.nodeTool)
		w.interrupted = err == nil
		return err
	})
	sc.Step(`^the run is resumed$`, func(ctx context.Context) error {
		w := world(ctx).Tool
		for _, f := range w.flows {
			key := tool.Key("run_"+f.name, f.name, 1, 1)
			raw, ok := w.journal[key]
			if !ok {
				return fmt.Errorf("flow %q has no recorded call", f.name)
			}
			if err := json.Unmarshal(raw, &w.resumed); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("no flow ran")
	})
	sc.Step(`^the resumed run receives the recorded result (\{.*\})$`, func(ctx context.Context, result string) error {
		got := world(ctx).Tool.resumed
		if got.Failed {
			return fmt.Errorf("the resumed run failed with %s: %s", got.Code, got.Message)
		}
		if !sameJSON(got.Result.Content, []byte(result)) {
			return fmt.Errorf("the resumed run received %s, want %s", got.Result.Content, result)
		}
		return nil
	})
	sc.Step(`^the resumed run receives the recorded error with code "([^"]*)" and message "([^"]*)"$`, func(ctx context.Context, code, message string) error {
		got := world(ctx).Tool.resumed
		if !got.Failed || string(got.Code) != code || got.Message != message {
			return fmt.Errorf("the resumed run received %+v, want %s: %s", got, code, message)
		}
		return nil
	})
	sc.Step(`^agent "([^"]*)" calls tool "([^"]*)" twice in one run$`, func(ctx context.Context, agent, name string) error {
		w := world(ctx).Tool
		set := w.setFor(agent)
		for index := 1; index <= 2; index++ {
			if _, err := w.runFlowStep(ctx, "run_"+agent, agent, 1, index, set, name); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the (\d+) calls ran as (\d+) steps with different idempotency keys$`, func(ctx context.Context, calls, steps int) error {
		keys := world(ctx).Tool.keys
		seen := map[string]bool{}
		for _, key := range keys {
			seen[key] = true
		}
		if len(keys) != calls || len(seen) != steps {
			return fmt.Errorf("%d calls made %d keys (%d distinct), want %d steps", len(keys), len(keys), len(seen), steps)
		}
		return nil
	})
	sc.Step(`^a run of flow "([^"]*)" executes the tool node$`, func(ctx context.Context, flow string) error {
		w := world(ctx).Tool
		f := w.flows[flow]
		if f == nil {
			return fmt.Errorf("no flow %q", flow)
		}
		_, err := w.runFlowStep(ctx, "run_"+flow, flow, 1, 1, w.setFrom(f.tools), f.nodeTool)
		return err
	})

	registerToolMCP(sc)
	registerToolSandbox(sc)
}

func registerToolMCP(sc *godog.ScenarioContext) {
	sc.Step(`^a simulated MCP server "([^"]*)"$`, func(ctx context.Context, name string) error {
		world(ctx).Tool.mcp(name)
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" offers tool "([^"]*)" requiring "([^"]*)"$`, func(ctx context.Context, server, name, required string) error {
		world(ctx).Tool.mcp(server).addTool(name, "", required, "")
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" offers tool "([^"]*)" described as "([^"]*)", requiring "([^"]*)", and tool "([^"]*)" described as "([^"]*)", requiring "([^"]*)"$`, func(ctx context.Context, server, first, firstDesc, firstReq, second, secondDesc, secondReq string) error {
		sim := world(ctx).Tool.mcp(server)
		sim.addTool(first, firstDesc, firstReq, "")
		sim.addTool(second, secondDesc, secondReq, "")
		return nil
	})
	sc.Step(`^agent "([^"]*)" uses the MCP server "([^"]*)"$`, func(ctx context.Context, agent, server string) error {
		return world(ctx).Tool.useMCP(ctx, agent, server)
	})
	sc.Step(`^agent "([^"]*)" uses the MCP server "([^"]*)" that offers tool "([^"]*)" requiring "([^"]*)" of type "([^"]*)", with credential "([^"]*)"$`, func(ctx context.Context, agent, server, name, required, typ, credential string) error {
		w := world(ctx).Tool
		sim := w.mcp(server)
		sim.credRef = "cred://" + credential
		sim.addTool(name, "", required, typ)
		return w.useMCP(ctx, agent, server)
	})
	sc.Step(`^agent "([^"]*)" uses the MCP server "([^"]*)" that offers tool "([^"]*)" requiring "([^"]*)", with credential "([^"]*)"$`, func(ctx context.Context, agent, server, name, required, credential string) error {
		w := world(ctx).Tool
		sim := w.mcp(server)
		sim.credRef = "cred://" + credential
		sim.addTool(name, "", required, "")
		return w.useMCP(ctx, agent, server)
	})
	sc.Step(`^agent "([^"]*)" uses the MCP server "([^"]*)" that offers tool "([^"]*)" requiring "([^"]*)" of type "([^"]*)"$`, func(ctx context.Context, agent, server, name, required, typ string) error {
		w := world(ctx).Tool
		w.mcp(server).addTool(name, "", required, typ)
		return w.useMCP(ctx, agent, server)
	})
	sc.Step(`^agent "([^"]*)" uses the MCP server "([^"]*)" that offers tool "([^"]*)" requiring "([^"]*)"$`, func(ctx context.Context, agent, server, name, required string) error {
		w := world(ctx).Tool
		w.mcp(server).addTool(name, "", required, "")
		return w.useMCP(ctx, agent, server)
	})
	sc.Step(`^the simulated MCP server "([^"]*)" answers "([^"]*)" with (\{.*\})$`, func(ctx context.Context, server, name, result string) error {
		world(ctx).Tool.mcp(server).answers[name] = json.RawMessage(result)
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" answers "([^"]*)" with the error "([^"]*)"$`, func(ctx context.Context, server, name, message string) error {
		world(ctx).Tool.mcp(server).failures[name] = message
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" goes offline$`, func(ctx context.Context, server string) error {
		sim := world(ctx).Tool.mcp(server)
		sim.mu.Lock()
		sim.offline = true
		sim.mu.Unlock()
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" drops the connection after receiving a call$`, func(ctx context.Context, server string) error {
		sim := world(ctx).Tool.mcp(server)
		sim.mu.Lock()
		sim.drop = true
		sim.mu.Unlock()
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" replaces tool "([^"]*)" with tool "([^"]*)" and reports its tool list changed$`, func(ctx context.Context, server, oldName, newName string) error {
		sim := world(ctx).Tool.mcp(server)
		sim.server.RemoveTools(oldName)
		sim.addTool(newName, "", "region", "")
		return nil
	})
	sc.Step(`^tool "([^"]*)" is described as "([^"]*)" and requires "([^"]*)"$`, func(ctx context.Context, name, description, required string) error {
		d := world(ctx).Tool.tools[name]
		if d == nil {
			return fmt.Errorf("no tool %q", name)
		}
		if d.Description != description {
			return fmt.Errorf("tool %q is described %q, want %q", name, d.Description, description)
		}
		return toolRequires(d.Input, required)
	})
	sc.Step(`^tool "([^"]*)" requires "([^"]*)"$`, func(ctx context.Context, name, required string) error {
		d := world(ctx).Tool.tools[name]
		if d == nil {
			return fmt.Errorf("no tool %q", name)
		}
		return toolRequires(d.Input, required)
	})
	sc.Step(`^the call succeeds with the result (\{.*\})$`, func(ctx context.Context, result string) error {
		w := world(ctx).Tool
		if w.lastErr != nil {
			return w.lastErr
		}
		if w.last.Failed {
			return fmt.Errorf("the call failed with %s: %s", w.last.Code, w.last.Message)
		}
		if !sameJSON(w.last.Result.Content, []byte(result)) {
			return fmt.Errorf("the call returned %s, want %s", w.last.Result.Content, result)
		}
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" received (\d+) calls? of "([^"]*)" with arguments (\{.*\})$`, func(ctx context.Context, server string, n int, name, args string) error {
		sim := world(ctx).Tool.mcp(server)
		if got := sim.callCount(name); got != n {
			return fmt.Errorf("server %q received %d calls of %q, want %d", server, got, name, n)
		}
		sim.mu.Lock()
		defer sim.mu.Unlock()
		for _, call := range sim.calls {
			if call.name == name && sameJSON(call.args, []byte(args)) {
				return nil
			}
		}
		return fmt.Errorf("server %q received no call of %q with %s", server, name, args)
	})
	sc.Step(`^the simulated MCP server "([^"]*)" received (\d+) calls? of "([^"]*)"$`, func(ctx context.Context, server string, n int, name string) error {
		if got := world(ctx).Tool.mcp(server).callCount(name); got != n {
			return fmt.Errorf("server %q received %d calls of %q, want %d", server, got, name, n)
		}
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" received (\d+) calls?$`, func(ctx context.Context, server string, n int) error {
		if got := world(ctx).Tool.mcp(server).totalCalls(); got != n {
			return fmt.Errorf("server %q received %d calls, want %d", server, got, n)
		}
		return nil
	})
	sc.Step(`^a credential "([^"]*)" whose secret is "([^"]*)"$`, func(ctx context.Context, name, secret string) error {
		w := world(ctx).Tool
		if err := w.creds.Put(ctx, name, []byte(secret)); err != nil {
			return err
		}
		w.secrets = append(w.secrets, secret)
		return nil
	})
	sc.Step(`^the simulated MCP server "([^"]*)" received the credential "([^"]*)"$`, func(ctx context.Context, server, secret string) error {
		if !world(ctx).Tool.mcp(server).receivedCredential(secret) {
			return fmt.Errorf("server %q did not receive the credential", server)
		}
		return nil
	})
	sc.Step(`^the secret "([^"]*)" appears in no run event, history entry, error or log line$`, func(ctx context.Context, secret string) error {
		w := world(ctx).Tool
		artifacts := append([]string{}, w.events...)
		artifacts = append(artifacts, w.history...)
		if w.lastErr != nil {
			artifacts = append(artifacts, w.lastErr.Error())
		}
		artifacts = append(artifacts, w.logBuf.String())
		for _, artifact := range artifacts {
			if strings.Contains(artifact, secret) {
				return fmt.Errorf("the secret appears in %q", artifact)
			}
		}
		return nil
	})
	sc.Step(`^listing the tools fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := world(ctx).Tool.listErr
		if err == nil {
			return fmt.Errorf("listing succeeded, want %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("listing failed with %s, want %s: %w", got, code, err)
		}
		return nil
	})
}

func (w *toolWorld) useMCP(ctx context.Context, agent, server string) error {
	sim := w.mcp(server)
	client := tool.NewMCPClient(tool.MCPServer{Name: server, CredRef: sim.credRef, Dial: sim.dial}, w.creds)
	tools, err := client.Tools(ctx, w.setFor(agent))
	if err != nil {
		w.listErr = err
		return nil
	}
	w.listErr = nil
	for _, offered := range tools {
		w.tools[offered.Name] = &toolDef{Tool: offered, mcpServer: server}
		w.giveTool(agent, offered.Name)
	}
	w.clients[server] = client
	a := w.agent(agent)
	found := false
	for _, existing := range a.servers {
		if existing == server {
			found = true
		}
	}
	if !found {
		a.servers = append(a.servers, server)
	}
	w.offered = w.setFor(agent).Names()
	return nil
}

func registerToolSandbox(sc *godog.ScenarioContext) {
	sc.Step(`^a simulated sandbox "([^"]*)"$`, func(ctx context.Context, name string) error {
		world(ctx).Tool.sandbox(name)
		return nil
	})
	sc.Step(`^agent "([^"]*)" is given tool "([^"]*)", which runs a command in sandbox "([^"]*)"$`, func(ctx context.Context, agent, name, sandbox string) error {
		w := world(ctx).Tool
		w.sandbox(sandbox)
		w.giveTool(agent, name)
		d := w.tools[name]
		d.sandbox = sandbox
		d.Input = toolSchema(map[string]any{"command": map[string]any{"type": "string"}}, []string{"command"}, false)
		return nil
	})
	sc.Step(`^the host environment defines "([^"]*)" and host file "([^"]*)" exists$`, func(ctx context.Context, env, file string) error {
		w := world(ctx).Tool
		w.hostEnv[env] = "top-secret"
		w.hostFiles[file] = true
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" has no network access$`, func(ctx context.Context, name string) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.network = nil
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" is granted network access to "([^"]*)"$`, func(ctx context.Context, name, host string) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.network = []string{host}
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" has a time limit of (\d+) seconds$`, func(ctx context.Context, name string, seconds int) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.timeLimit = time.Duration(seconds) * time.Second
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" has an output limit of (\d+) KiB$`, func(ctx context.Context, name string, kib int) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.maxOutput = kib * 1024
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" runs the command "([^"]*)" until it is stopped$`, func(ctx context.Context, name, command string) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.runUntilStopped = true
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" cannot start$`, func(ctx context.Context, name string) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.cannotStart = true
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" answers the command "([^"]*)" with exit status (\d+) and stdout "([^"]*)"$`, func(ctx context.Context, name, command string, exit int, stdout string) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.answers[command] = bddSandboxAnswer{exit: exit, stdout: []byte(stdout)}
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^sandbox "([^"]*)" answers the command "([^"]*)" with exit status (\d+) and (\d+) bytes of output$`, func(ctx context.Context, name, command string, exit, size int) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		sb.answers[command] = bddSandboxAnswer{exit: exit, stdout: bytes.Repeat([]byte("x"), size)}
		sb.mu.Unlock()
		return nil
	})
	sc.Step(`^agent "([^"]*)" calls tool "([^"]*)" with the command "([^"]*)"$`, func(ctx context.Context, agent, name, command string) error {
		args, _ := json.Marshal(map[string]string{"command": command})
		return world(ctx).Tool.call(ctx, agent, name, args)
	})
	sc.Step(`^the sandbox "([^"]*)" received (\d+) host environment variables?, (\d+) host files?, no instance metadata access and no network grant$`, func(ctx context.Context, name string, envs, files int) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		spec := sb.gotSpec
		sb.mu.Unlock()
		if len(spec.Files) != files {
			return fmt.Errorf("the sandbox received %d host files, want %d", len(spec.Files), files)
		}
		if len(spec.Network) != 0 {
			return fmt.Errorf("the sandbox received network %v, want none", spec.Network)
		}
		if len(spec.CredRefs) != envs {
			return fmt.Errorf("the sandbox received %d environment variables, want %d", len(spec.CredRefs), envs)
		}
		return nil
	})
	sc.Step(`^the sandbox "([^"]*)" received the network grant "([^"]*)" and nothing else$`, func(ctx context.Context, name, host string) error {
		sb := world(ctx).Tool.sandbox(name)
		sb.mu.Lock()
		network := append([]string(nil), sb.gotSpec.Network...)
		sb.mu.Unlock()
		if len(network) != 1 || network[0] != host {
			return fmt.Errorf("the sandbox received network %v, want only %q", network, host)
		}
		return nil
	})
	sc.Step(`^the call's output is "([^"]*)"$`, func(ctx context.Context, want string) error {
		if got := outputText(world(ctx).Tool.last.Result.Content); got != want {
			return fmt.Errorf("output %q, want %q", got, want)
		}
		return nil
	})
	sc.Step(`^the call's output does not contain "([^"]*)"$`, func(ctx context.Context, unwanted string) error {
		if got := outputText(world(ctx).Tool.last.Result.Content); strings.Contains(got, unwanted) {
			return fmt.Errorf("output %q contains %q", got, unwanted)
		}
		return nil
	})
	sc.Step(`^the call's output is the first (\d+) bytes$`, func(ctx context.Context, size int) error {
		got := outputText(world(ctx).Tool.last.Result.Content)
		if len(got) != size {
			return fmt.Errorf("output has %d bytes, want %d", len(got), size)
		}
		return nil
	})
	sc.Step(`^the call's output is marked truncated$`, func(ctx context.Context) error {
		if !world(ctx).Tool.last.Result.Truncated {
			return fmt.Errorf("output is not marked truncated")
		}
		return nil
	})
	sc.Step(`^the call succeeds with exit status (\d+) and output "([^"]*)"$`, func(ctx context.Context, exit int, output string) error {
		w := world(ctx).Tool
		if w.last.Failed {
			return fmt.Errorf("the call failed with %s: %s", w.last.Code, w.last.Message)
		}
		if w.last.Result.ExitStatus != exit {
			return fmt.Errorf("exit status %d, want %d", w.last.Result.ExitStatus, exit)
		}
		if got := outputText(w.last.Result.Content); got != output {
			return fmt.Errorf("output %q, want %q", got, output)
		}
		return nil
	})
	sc.Step(`^the sandbox "([^"]*)" received (\d+) commands?$`, func(ctx context.Context, name string, n int) error {
		if got := world(ctx).Tool.sandbox(name).received(); got != n {
			return fmt.Errorf("the sandbox received %d commands, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the sandbox "([^"]*)" stopped the command$`, func(ctx context.Context, name string) error {
		sb := world(ctx).Tool.sandbox(name)
		<-sb.stopCh
		sb.mu.Lock()
		stopped := sb.stopped
		sb.mu.Unlock()
		if !stopped {
			return fmt.Errorf("the sandbox did not stop the command")
		}
		return nil
	})
}

func toolRequires(input json.RawMessage, name string) error {
	required, err := requiredOf(input)
	if err != nil {
		return err
	}
	for _, item := range required {
		if item == name {
			return nil
		}
	}
	return fmt.Errorf("schema requires %v, want %q", required, name)
}

func offeredExactly(offered []string, want ...string) error {
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if len(offered) != len(sorted) {
		return fmt.Errorf("the model is offered %v, want %v", offered, sorted)
	}
	for i := range sorted {
		if offered[i] != sorted[i] {
			return fmt.Errorf("the model is offered %v, want %v", offered, sorted)
		}
	}
	return nil
}

func callFailed(w *toolWorld, code, message string) error {
	if w.lastErr != nil {
		return w.lastErr
	}
	if !w.last.Failed || string(w.last.Code) != code {
		return fmt.Errorf("the call ended with %+v, want %s", w.last, code)
	}
	if message != "" && w.last.Message != message {
		return fmt.Errorf("the call message %q, want %q", w.last.Message, message)
	}
	return nil
}

func errorNames(message, name, requirement string) error {
	if !strings.Contains(message, `"`+name+`"`) {
		return fmt.Errorf("the error %q does not name %q", message, name)
	}
	if !strings.Contains(message, requirement) {
		return fmt.Errorf("the error %q does not say %q", message, requirement)
	}
	return nil
}

var _ platform.Credentials = (*bddCredentials)(nil)
