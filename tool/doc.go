// Package tool defines the tool port, per-caller allowlists, the call step,
// the MCP client adapter (github.com/modelcontextprotocol/go-sdk) and the
// sandbox port for generated code.
//
// A Tool has a name, a description and an input JSON Schema. Set is one
// caller's allowlist. Layer.Step runs one call as a step function: it checks
// the allowlist, validates the arguments with core/schema, runs the tool under
// its time limit with panic recovery and stores an oversized result in blob
// storage. An Outcome records success or the code and message a model sees; it
// is returned as a step result, so a resumed run never executes a completed
// call again (REQ-TOOL-003, ADR-0011).
//
// MCPClient lists a server's tools under mcp_<server>__<tool> names and
// forwards calls over the MCP Go SDK. Sandbox is the port for commands run
// outside the host; Layer.RunSandbox enforces the time and output limits
// around a driver.
//
// Status: implemented (WP-L7-02). Concrete sandbox drivers are a follow-up
// work package; the gate exercises the port against a simulated sandbox.
// See specs/tools/design.md.
package tool
