package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// MCPServer configures one MCP server. Dial opens a transport to the server;
// when CredRef is set, the credential is resolved at the point of use and the
// secret is passed to Dial only (REQ-TOOL-017).
type MCPServer struct {
	Name    string
	CredRef string // "cred://<name>", optional
	Dial    func(ctx context.Context, credential []byte) (mcp.Transport, error)
}

// MCPClient lists one server's tools and forwards calls to it. Tool names are
// mcp_<server>__<tool>, with every character of the server name outside
// [A-Za-z0-9] replaced by '_' (REQ-TOOL-012).
type MCPClient struct {
	server MCPServer
	creds  platform.Credentials

	mu sync.Mutex
}

// NewMCPClient returns a client for server. creds may be nil when the server
// names no credential.
func NewMCPClient(server MCPServer, creds platform.Credentials) *MCPClient {
	return &MCPClient{server: server, creds: creds}
}

// MCPToolName is the name under which a server's tool is offered:
// mcp_<server>__<tool>, the server part sanitized to [A-Za-z0-9_].
func MCPToolName(server, tool string) string {
	return "mcp_" + sanitizeServer(server) + "__" + tool
}

// sanitizeServer maps a server name to the name segment used in a tool name.
// A run of characters outside [A-Za-z0-9] becomes one '_', so the segment can
// never contain the "__" separator that ends the server part. Two servers can
// still sanitize to the same segment; Set.Merge rejects the resulting
// duplicate tool names instead of letting Get pick the first.
func sanitizeServer(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return b.String()
}

// Tools returns the server's tools with prefixed names, descriptions and input
// schemas. A name that collides with a tool already in existing is rejected
// with code conflict (REQ-TOOL-024).
//
// The list is read from the server before every offer (REQ-TOOL-016), so a
// server that changed its tools — with or without a "list changed"
// notification — is always seen by the next model turn. Re-reading is the
// deterministic option the design allows; a cached list keyed on the SDK's
// notification would race the gate's clock.
func (c *MCPClient) Tools(ctx context.Context, existing *Set) ([]Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	credential, err := c.credential(ctx)
	if err != nil {
		return nil, err
	}
	session, err := c.connect(ctx, credential)
	if err != nil {
		return nil, err
	}
	// The listing session is not kept: every offer reads the server afresh
	// (REQ-TOOL-016), so holding its connection would leak one per offer.
	defer func() { _ = session.Close() }()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, mcpError(c.server.Name, "list tools", err, string(credential))
	}

	tools := make([]Tool, 0, len(result.Tools))
	seen := map[string]bool{}
	for _, remote := range result.Tools {
		if remote == nil {
			continue
		}
		name := MCPToolName(c.server.Name, remote.Name)
		if seen[name] {
			return nil, core.NewError(core.CodeConflict,
				fmt.Sprintf("mcp server %q lists %q twice", c.server.Name, name), nil)
		}
		seen[name] = true
		if _, ok := existing.Get(name); ok {
			return nil, core.NewError(core.CodeConflict,
				fmt.Sprintf("mcp server %q offers %q, which is already in the caller's tools", c.server.Name, name),
				map[string]any{"tool": name})
		}
		input := json.RawMessage(`{}`)
		if remote.InputSchema != nil {
			raw, err := json.Marshal(remote.InputSchema)
			if err == nil {
				input = raw
			}
		}
		tools = append(tools, Tool{
			Name:        name,
			Description: remote.Description,
			Input:       input,
			Run:         c.runner(remote.Name),
		})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}

// Call forwards one call to the server. The credential, when configured, is
// resolved at the call and never recorded. A call that cannot be forwarded is
// unavailable; a failure the server reports is vendor_error
// (REQ-TOOL-013…015, REQ-TOOL-025).
func (c *MCPClient) Call(ctx context.Context, call Call) (Result, error) {
	remote, ok := c.remoteName(call.Name)
	if !ok {
		return Result{}, core.NewError(core.CodeNotFound,
			fmt.Sprintf("mcp server %q does not offer %q", c.server.Name, call.Name), nil)
	}
	credential, err := c.credential(ctx)
	if err != nil {
		return Result{}, err
	}
	session, err := c.connect(ctx, credential)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = session.Close() }()

	var args any
	if len(call.Args) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(call.Args))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return Result{}, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("tool %q: arguments are not valid JSON", call.Name), nil)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return Result{}, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("tool %q: arguments are not valid JSON", call.Name), nil)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: remote, Arguments: args})
	if err != nil {
		return Result{}, mcpError(c.server.Name, "call "+remote, err, string(credential))
	}
	if result.IsError {
		message := scrubSecret(contentText(result.Content), string(credential))
		if len(message) > DefaultMaxResult {
			message = message[:DefaultMaxResult]
		}
		return Result{}, core.NewError(core.CodeVendor, message, map[string]any{
			"server": c.server.Name,
			"tool":   call.Name,
		})
	}
	content, err := contentJSON(result)
	if err != nil {
		return Result{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("mcp server %q returned a result that is not JSON", c.server.Name), nil)
	}
	return Result{Content: scrubJSON(content, string(credential))}, nil
}

// Close releases any resources held by the client. Listings and calls open
// and close their own session, so there is nothing left to close.
func (c *MCPClient) Close() error { return nil }

func (c *MCPClient) connect(ctx context.Context, credential []byte) (*mcp.ClientSession, error) {
	if c.server.Dial == nil {
		return nil, core.NewError(core.CodeUnavailable,
			fmt.Sprintf("mcp server %q is unreachable", c.server.Name), nil)
	}
	transport, err := c.server.Dial(ctx, credential)
	if err != nil {
		// Never echo the dial error: a transport may include headers.
		return nil, core.NewError(core.CodeUnavailable,
			fmt.Sprintf("mcp server %q is unreachable", c.server.Name), nil)
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "basamento", Version: "0"}, nil,
	)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable,
			fmt.Sprintf("mcp server %q is unreachable", c.server.Name), nil)
	}
	return session, nil
}

// credential resolves the server's reference at the point of use. The value
// never appears in an error: a failure is reported without the secret, the
// reference's value or the store's error text.
func (c *MCPClient) credential(ctx context.Context) ([]byte, error) {
	if c.server.CredRef == "" {
		return nil, nil
	}
	if c.creds == nil {
		return nil, core.NewError(core.CodeUnauthenticated,
			fmt.Sprintf("mcp server %q: no credential store", c.server.Name), nil)
	}
	secret, err := c.creds.Resolve(ctx, c.server.CredRef)
	if err != nil {
		code := core.CodeOf(err)
		if code != core.CodeUnavailable && code != core.CodeNotFound {
			code = core.CodeUnauthenticated
		}
		return nil, core.NewError(code,
			fmt.Sprintf("mcp server %q: its credential could not be resolved", c.server.Name), nil)
	}
	return secret, nil
}

func (c *MCPClient) runner(remote string) Func {
	name := MCPToolName(c.server.Name, remote)
	return func(ctx context.Context, args json.RawMessage) (Result, error) {
		return c.Call(ctx, Call{Name: name, Args: args})
	}
}

func (c *MCPClient) remoteName(name string) (string, bool) {
	prefix := "mcp_" + sanitizeServer(c.server.Name) + "__"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	return strings.TrimPrefix(name, prefix), true
}

// mcpError maps an SDK error to the platform's codes: a JSON-RPC error is the
// server's own answer (vendor_error); anything else is a lost or failed
// connection (unavailable), never retried (REQ-TOOL-014, REQ-TOOL-015).
func mcpError(server, op string, err error, secret string) error {
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		return core.NewError(core.CodeVendor, scrubSecret(rpcErr.Message, secret), map[string]any{
			"server": server,
			"op":     op,
		})
	}
	return core.NewError(core.CodeUnavailable,
		fmt.Sprintf("mcp server %q is unavailable", server), nil)
}

// scrubSecret removes a resolved credential from text the server controls.
// The credential is never written to an event, a history entry, an error or a
// log, even when the server echoes it back.
func scrubSecret(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

func scrubJSON(raw json.RawMessage, secret string) json.RawMessage {
	if secret == "" || len(raw) == 0 {
		return raw
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return json.RawMessage(scrubSecret(string(raw), secret))
	}
	out, err := json.Marshal(scrubValue(value, secret))
	if err != nil {
		return raw
	}
	return out
}

func scrubValue(value any, secret string) any {
	switch v := value.(type) {
	case string:
		return scrubSecret(v, secret)
	case []any:
		for i := range v {
			v[i] = scrubValue(v[i], secret)
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[scrubSecret(key, secret)] = scrubValue(item, secret)
		}
		return out
	default:
		return value
	}
}

// contentJSON turns the server's content into the tool result. A single text
// item that is itself JSON is kept as JSON (so {"rain_mm": 4} stays an
// object); other text is a JSON string.
func contentJSON(result *mcp.CallToolResult) (json.RawMessage, error) {
	if len(result.Content) == 0 {
		if result.StructuredContent != nil {
			return json.Marshal(result.StructuredContent)
		}
		return json.RawMessage(`null`), nil
	}
	if len(result.Content) == 1 {
		if text, ok := result.Content[0].(*mcp.TextContent); ok {
			trimmed := strings.TrimSpace(text.Text)
			if json.Valid([]byte(trimmed)) {
				return json.RawMessage(trimmed), nil
			}
			return json.Marshal(text.Text)
		}
	}
	items := make([]json.RawMessage, 0, len(result.Content))
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			raw, err := json.Marshal(text.Text)
			if err != nil {
				return nil, err
			}
			items = append(items, raw)
			continue
		}
		raw, err := json.Marshal(content)
		if err != nil {
			return nil, err
		}
		items = append(items, raw)
	}
	return json.Marshal(items)
}

// contentText is the message of a result the server marked as an error.
func contentText(content []mcp.Content) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if text, ok := item.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	if len(parts) == 0 {
		return "the MCP server returned a tool error"
	}
	return strings.Join(parts, "\n")
}
