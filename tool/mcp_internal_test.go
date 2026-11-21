package tool

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/epzero/basamento/core"
)

// A schema that cannot be serialized must fail the listing: registering the
// tool with Input: {} would silently disable its argument validation.
func TestMCPInputSchemaFailureIsInternal(t *testing.T) {
	client := NewMCPClient(MCPServer{Name: "weather"}, nil)
	_, err := client.inputSchema(&mcp.Tool{Name: "forecast", InputSchema: map[string]any{"city": func() {}}})
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("error %v, want internal", err)
	}
}

func TestMCPInputSchemaKeepsTheServersSchema(t *testing.T) {
	client := NewMCPClient(MCPServer{Name: "weather"}, nil)
	input, err := client.inputSchema(&mcp.Tool{Name: "forecast"})
	if err != nil || string(input) != "{}" {
		t.Fatalf("empty schema: input %s error %v, want {}", input, err)
	}
	input, err = client.inputSchema(&mcp.Tool{Name: "forecast", InputSchema: map[string]any{"type": "object"}})
	if err != nil || string(input) != `{"type":"object"}` {
		t.Fatalf("schema: input %s error %v", input, err)
	}
}

// scrubSecret must cover the encodings a server commonly echoes a credential
// in, not only the literal secret.
func TestScrubSecretCoversCommonEncodings(t *testing.T) {
	const secret = "p@ss word/+"
	forms := map[string]string{
		"literal":      secret,
		"base64":       "cEBzcyB3b3JkLys=",
		"hex":          "7040737320776f72642f2b",
		"query escape": "p%40ss+word%2F%2B",
		"path escape":  "p@ss%20word%2F+",
	}
	for name, form := range forms {
		scrubbed := scrubSecret("echo: "+form, secret)
		if strings.Contains(scrubbed, form) {
			t.Fatalf("%s form survived scrubbing: %q", name, scrubbed)
		}
		if !strings.Contains(scrubbed, "[redacted]") {
			t.Fatalf("%s form was not redacted: %q", name, scrubbed)
		}
	}
}
