package tool_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/tool"
)

// mcpSim is a simulated MCP server over the SDK's in-memory transport. It is
// the deterministic stand-in for a vendor server; the gate never dials out.
type mcpSim struct {
	server   *mcp.Server
	mu       sync.Mutex
	calls    []mcpCall
	answer   map[string]json.RawMessage
	fail     map[string]string
	drop     bool
	offline  bool
	creds    [][]byte
	closes   int
	lastLink *dropTransport
}

type mcpCall struct {
	name string
	args json.RawMessage
}

// dropTransport keeps the client-side connection so a simulated server can
// drop it mid-call: closing the client end never waits for the server handler.
type dropTransport struct {
	mcp.Transport
	mu      sync.Mutex
	conn    mcp.Connection
	onClose func()
}

func (t *dropTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	if t.onClose != nil {
		return &trackingConn{Connection: conn, onClose: t.onClose}, nil
	}
	return conn, nil
}

// trackingConn reports when the SDK closes a connection.
type trackingConn struct {
	mcp.Connection
	onClose func()
}

func (c *trackingConn) Close() error {
	c.onClose()
	return c.Connection.Close()
}

func (t *dropTransport) drop() {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func newMCPSim() *mcpSim {
	s := &mcpSim{answer: map[string]json.RawMessage{}, fail: map[string]string{}}
	s.server = mcp.NewServer(
		&mcp.Implementation{Name: "sim", Version: "0"},
		&mcp.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
	)
	return s
}

func (s *mcpSim) addTool(name, description, property string) {
	prop := map[string]any{}
	if property != "" {
		prop["type"] = "string"
	}
	input := map[string]any{
		"type":       "object",
		"properties": map[string]any{property: prop},
		"required":   []string{property},
	}
	s.server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: input}, s.handler(name))
}

func (s *mcpSim) removeTool(name string) { s.server.RemoveTools(name) }

func (s *mcpSim) handler(name string) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		s.mu.Lock()
		s.calls = append(s.calls, mcpCall{name: name, args: req.Params.Arguments})
		drop := s.drop
		fail := s.fail[name]
		answer := s.answer[name]
		link := s.lastLink
		s.mu.Unlock()
		if drop {
			if link != nil {
				link.drop()
			}
			return nil, errors.New("connection dropped")
		}
		if fail != "" {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: fail}},
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

func (s *mcpSim) dial(ctx context.Context, credential []byte) (mcp.Transport, error) {
	s.mu.Lock()
	offline := s.offline
	s.creds = append(s.creds, credential)
	s.mu.Unlock()
	if offline {
		return nil, errors.New("offline")
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	link := &dropTransport{Transport: clientTransport, onClose: func() {
		s.mu.Lock()
		s.closes++
		s.mu.Unlock()
	}}
	s.mu.Lock()
	s.lastLink = link
	s.mu.Unlock()
	go func() { _ = s.server.Run(context.Background(), serverTransport) }()
	return link, nil
}

func (s *mcpSim) callCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, call := range s.calls {
		if call.name == name {
			count++
		}
	}
	return count
}

func (s *mcpSim) closedConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

func (s *mcpSim) lastCredential() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.creds) == 0 {
		return nil
	}
	return s.creds[len(s.creds)-1]
}

func TestMCPListsToolsWithPrefixDescriptionAndSchema(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "Forecasts rain for a city", "city")
	sim.addTool("alerts", "Lists active alerts", "region")
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)

	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "mcp_weather__alerts,mcp_weather__forecast" {
		t.Fatalf("names %v", names)
	}
	forecast := tools[1]
	if forecast.Description != "Forecasts rain for a city" {
		t.Fatalf("description %q", forecast.Description)
	}
	var doc map[string]any
	if err := json.Unmarshal(forecast.Input, &doc); err != nil {
		t.Fatalf("schema %s: %v", forecast.Input, err)
	}
	required, _ := doc["required"].([]any)
	if len(required) != 1 || required[0] != "city" {
		t.Fatalf("schema %s does not require city", forecast.Input)
	}
}

func TestMCPForwardsACallAndReturnsItsResult(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "Forecasts rain for a city", "city")
	sim.answer["forecast"] = json.RawMessage(`{"rain_mm": 4}`)
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	res, err := tools[0].Run(context.Background(), json.RawMessage(`{"city":"Porto"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Content) != `{"rain_mm": 4}` {
		t.Fatalf("result %s", res.Content)
	}
	if sim.callCount("forecast") != 1 {
		t.Fatalf("server received %d calls", sim.callCount("forecast"))
	}
}

func TestMCPRejectsACollidingPrefixedName(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	existing := &tool.Set{Tools: []tool.Tool{{Name: "mcp_weather__forecast"}}}
	_, err := client.Tools(context.Background(), existing)
	if err == nil || core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error %v, want conflict", err)
	}
}

func TestMCPUnreachableServerIsUnavailable(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	sim.offline = true
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	_, err := client.Tools(context.Background(), nil)
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("error %v, want unavailable", err)
	}
	if sim.callCount("forecast") != 0 {
		t.Fatal("an unreachable server received a call")
	}
}

func TestMCPDroppedConnectionIsUnavailable(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	sim.mu.Lock()
	sim.drop = true
	sim.mu.Unlock()
	_, err = tools[0].Run(context.Background(), json.RawMessage(`{"city":"Porto"}`))
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("error %v, want unavailable", err)
	}
	if sim.callCount("forecast") != 1 {
		t.Fatalf("server received %d calls", sim.callCount("forecast"))
	}
}

func TestMCPToolErrorIsVendorError(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	sim.fail["forecast"] = "station offline"
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	_, err = tools[0].Run(context.Background(), json.RawMessage(`{"city":"Porto"}`))
	if err == nil || core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("error %v, want vendor_error", err)
	}
	if !strings.Contains(err.Error(), "station offline") {
		t.Fatalf("error %q does not carry the server's message", err)
	}
}

func TestMCPProtocolErrorIsVendorError(t *testing.T) {
	sim := newMCPSim()
	sim.server.AddTool(&mcp.Tool{
		Name:        "broken",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("the server broke")
	})
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	_, err = tools[0].Run(context.Background(), json.RawMessage(`{}`))
	if err == nil || core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("error %v, want vendor_error", err)
	}
	if !strings.Contains(err.Error(), "the server broke") {
		t.Fatalf("error %q does not carry the server's message", err)
	}
}

func TestMCPListIsReadAgainAfterAChange(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	first, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if first[0].Name != "mcp_weather__forecast" {
		t.Fatalf("first list %v", first)
	}
	sim.removeTool("forecast")
	sim.addTool("radar", "", "region")
	second, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools after change: %v", err)
	}
	if len(second) != 1 || second[0].Name != "mcp_weather__radar" {
		t.Fatalf("second list %v", second)
	}
}

func TestMCPCredentialIsResolvedAtTheCall(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	creds := &memCredentials{values: map[string][]byte{"cred://weather-key": []byte("sk-weather-7f3a")}}
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", CredRef: "cred://weather-key", Dial: sim.dial}, creds)
	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if _, err := tools[0].Run(context.Background(), json.RawMessage(`{"city":"Porto"}`)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(sim.lastCredential()); got != "sk-weather-7f3a" {
		t.Fatalf("server received credential %q", got)
	}
	if creds.resolves < 1 {
		t.Fatal("the credential was never resolved")
	}
}

func TestMCPMissingCredentialLeaksNoSecret(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	creds := &memCredentials{values: map[string][]byte{}}
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", CredRef: "cred://weather-key", Dial: sim.dial}, creds)
	_, err := client.Tools(context.Background(), nil)
	if err == nil {
		t.Fatal("Tools succeeded without the credential")
	}
	if strings.Contains(err.Error(), "weather-key") && strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error leaks the secret: %q", err)
	}
}

func TestMCPToolNameSanitizesTheServer(t *testing.T) {
	tests := map[string]string{
		"weather":   "mcp_weather__forecast",
		"my server": "mcp_my_server__forecast",
		"a/b.c":     "mcp_a_b_c__forecast",
		"w-1":       "mcp_w_1__forecast",
	}
	for server, want := range tests {
		if got := tool.MCPToolName(server, "forecast"); got != want {
			t.Errorf("MCPToolName(%q) = %q, want %q", server, got, want)
		}
	}
	pattern := regexp.MustCompile(`^mcp_[A-Za-z0-9_]+__forecast$`)
	for _, server := range []string{" ", "ÜBER", "a b-c.d/e", "日本語"} {
		got := tool.MCPToolName(server, "forecast")
		if !pattern.MatchString(got) {
			t.Errorf("MCPToolName(%q) = %q does not match the naming rule", server, got)
		}
		if strings.HasSuffix(got, "__forecast") == false {
			t.Errorf("MCPToolName(%q) = %q lost the tool suffix", server, got)
		}
	}
}

// memCredentials is a platform.Credentials for tests.
type memCredentials struct {
	mu       sync.Mutex
	values   map[string][]byte
	resolves int
}

func (c *memCredentials) Put(_ context.Context, name string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values["cred://"+name] = append([]byte(nil), value...)
	return nil
}

func (c *memCredentials) Resolve(_ context.Context, ref string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolves++
	value, ok := c.values[ref]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no credential", nil)
	}
	return value, nil
}

func (c *memCredentials) Delete(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, "cred://"+name)
	return nil
}

func (c *memCredentials) Names(context.Context) ([]string, error) { return nil, nil }

func TestMCPArgumentsKeepLargeIntegers(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("echo", "", "id")
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if _, err := tools[0].Run(context.Background(), json.RawMessage(`{"id":9007199254740993}`)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sim.mu.Lock()
	defer sim.mu.Unlock()
	if len(sim.calls) != 1 {
		t.Fatalf("server received %d calls", len(sim.calls))
	}
	if !strings.Contains(string(sim.calls[0].args), "9007199254740993") {
		t.Fatalf("server received %s, want the exact integer", sim.calls[0].args)
	}
}

func TestMCPListingSessionIsClosedAfterTheList(t *testing.T) {
	sim := newMCPSim()
	sim.addTool("forecast", "", "city")
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", Dial: sim.dial}, nil)
	if _, err := client.Tools(context.Background(), nil); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if got := sim.closedConnections(); got != 1 {
		t.Fatalf("%d listing connections closed, want 1", got)
	}
	if _, err := client.Tools(context.Background(), nil); err != nil {
		t.Fatalf("second Tools: %v", err)
	}
	if got := sim.closedConnections(); got != 2 {
		t.Fatalf("%d listing connections closed after two offers, want 2", got)
	}
}

func TestMCPToolNameServerPartHasNoSeparator(t *testing.T) {
	for _, server := range []string{"a__b", "a  b", "a_-b", "pay_eu"} {
		name := tool.MCPToolName(server, "forecast")
		serverPart := strings.TrimSuffix(strings.TrimPrefix(name, "mcp_"), "__forecast")
		if strings.Contains(serverPart, "__") {
			t.Errorf("MCPToolName(%q) = %q: the server part contains the separator", server, name)
		}
	}
}

func TestSetMergeRejectsDuplicateNames(t *testing.T) {
	first := &tool.Set{Tools: []tool.Tool{{Name: "mcp_pay_eu__charge"}}}
	second := &tool.Set{Tools: []tool.Tool{{Name: "mcp_pay_eu__charge"}}}
	err := first.Merge(second)
	if err == nil || core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error %v, want conflict", err)
	}
	if len(first.Tools) != 1 {
		t.Fatalf("the set changed on a rejected merge: %v", first.Tools)
	}
}

func TestMCPEchoedCredentialIsScrubbed(t *testing.T) {
	const secret = "sk-weather-7f3a"
	forms := map[string]string{
		"literal": secret,
		"base64":  base64.StdEncoding.EncodeToString([]byte(secret)),
		"hex":     hex.EncodeToString([]byte(secret)),
		"url":     url.QueryEscape(secret),
	}
	answer, err := json.Marshal(forms)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	sim := newMCPSim()
	sim.addTool("echo", "", "city")
	sim.answer["echo"] = answer
	creds := &memCredentials{values: map[string][]byte{"cred://weather-key": []byte(secret)}}
	client := tool.NewMCPClient(tool.MCPServer{Name: "weather", CredRef: "cred://weather-key", Dial: sim.dial}, creds)
	tools, err := client.Tools(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	res, err := tools[0].Run(context.Background(), json.RawMessage(`{"city":"Porto"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for name, form := range forms {
		if strings.Contains(string(res.Content), form) {
			t.Fatalf("result %s carries the credential as %s", res.Content, name)
		}
	}
	if !strings.Contains(string(res.Content), "[redacted]") {
		t.Fatalf("result %s is not scrubbed", res.Content)
	}

	sim.mu.Lock()
	sim.fail["echo"] = "bad key " + secret + " / " + forms["base64"] + " / " + forms["hex"]
	sim.mu.Unlock()
	_, err = tools[0].Run(context.Background(), json.RawMessage(`{"city":"Porto"}`))
	if err == nil || core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("error %v, want vendor_error", err)
	}
	for name, form := range forms {
		if strings.Contains(err.Error(), form) {
			t.Fatalf("error %q carries the credential as %s", err, name)
		}
	}
}
