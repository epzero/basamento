# Tools: the tool port, allowlists, MCP and sandboxes

Status: **Planned** (WP-L7-02). Roadmap: `docs/roadmap/L7-agents.md`. Design:
`design.md`, ADR-0006, ADR-0011, ADR-0012.

A **tool** is a named operation an agent or a flow's `tool` node may call:
it has a name, a description and an input JSON Schema, and runs with
arguments that satisfy that schema. Tools reach the platform three ways: one
offered in process, a set discovered from an **MCP server**, or a command run
in a **sandbox**. Every call is one durable step (ADR-0011), so a resumed run
never executes a completed call again.

Who decides to call a tool is the agent loop (WP-L7-01, `specs/agent`). Tools
that workers on other machines serve are `specs/models/workers.feature`
(WP-L3-07), and this capability does not change what a worker receives.
Credentials are the vault's (WP-L1-06) and JSON Schema validation is that of
`core/schema` (WP-L2-02); this capability uses both.

## The tool port

### REQ-TOOL-001 — A tool's description
The tool port shall describe every tool by its name, its description and its input JSON Schema.

### REQ-TOOL-002 — Arguments are checked before the tool runs
If a call's arguments do not satisfy the tool's input schema, then the tool layer shall not run the tool and shall report the violation, naming the argument and what the schema requires of it, as the tool's error.

### REQ-TOOL-003 — A call is one durable step that runs once
The tool layer shall execute each tool call as one durable step whose idempotency key ties it to its run, node and call index, so that a resumed run returns the recorded result or error without running the tool again.

### REQ-TOOL-004 — A tool that fails
When a tool returns an error, the tool layer shall end the call with that tool's error code and message without retrying the call.

### REQ-TOOL-005 — A tool that panics
If a tool panics while it runs, then the tool layer shall catch the panic, end the call with code `internal` and the tool's name, and let the run continue.

### REQ-TOOL-006 — A tool that exceeds its time limit
If a tool call exceeds its time limit, then the tool layer shall end the call with code `timeout` and end the tool at its next heartbeat.

### REQ-TOOL-007 — A result that is too large
If a tool's result exceeds the result size limit, then the tool layer shall store the result in blob storage and return its reference in place of it.

## Allowlists

### REQ-TOOL-008 — An agent calls only its tools
If an agent or a flow calls a tool that is not on its allowlist, then the tool layer shall refuse the call with code `permission_denied`, not run the tool, and report the refusal to the caller.

### REQ-TOOL-023 — A model sees only its caller's allowlist
The tool layer shall offer to a caller's model only the tools on that caller's allowlist.

### REQ-TOOL-009 — Changes to a tool's set
The tool layer shall offer to an agent whose allowlist changed only the tools of its current allowlist, from its next model turn.

## Output back to a model

### REQ-TOOL-011 — A failure goes to a model as an error
When a tool call ends with an error, the tool layer shall give the model that error's code and message, without the tool's error details.

## The MCP client

### REQ-TOOL-012 — Tools found on an MCP server
When an MCP client connects to a server, the tool layer shall offer the server's tools by name, with their descriptions and input schemas, under names prefixed with the server's name.

### REQ-TOOL-013 — A call forwarded to the MCP server
When a call to a tool of an MCP server satisfies its schema, the tool layer shall forward the call to that server and return its result as the tool result.

### REQ-TOOL-014 — An unreachable MCP server
If an MCP server cannot be reached, then the tool layer shall fail calls to its tools before forwarding them, with code `unavailable`.

### REQ-TOOL-015 — An MCP server that drops a call
If an MCP server's connection is lost after the tool layer forwarded a call, then the tool layer shall end the call with code `unavailable`.

### REQ-TOOL-016 — An MCP server that changes its tools
When an MCP server reports that its tool list changed, the tool layer shall re-read the server's tools before offering them to an agent again.

### REQ-TOOL-017 — MCP server credentials
When calling an MCP server that configures a credential reference, the tool layer shall resolve the credential from that reference at the call and never write the secret to a run's events, history, errors or logs.

### REQ-TOOL-024 — Conflicting MCP tool names
If an MCP server offers a tool whose prefixed name collides with a tool already in the caller's set, then the tool layer shall reject the tool list with code `conflict`.

### REQ-TOOL-025 — An MCP server that returns a tool error
If an MCP server returns an error for a tool call, then the tool layer shall end the call with code `vendor_error` and the server's message.

## The sandbox port

### REQ-TOOL-018 — What a sandbox can reach
The sandbox port shall run a command with no access to the host's files, its environment variables and its instance metadata service, and with no network access unless the sandbox was granted one.

### REQ-TOOL-019 — A command that exceeds the sandbox's time limit
If a command exceeds its sandbox's time limit, then the sandbox port shall stop the command and end the call with code `timeout`.

### REQ-TOOL-020 — Output beyond the sandbox's output limit
If a command's output exceeds its sandbox's output limit, then the sandbox port shall keep the first bytes up to the limit, discard the rest and mark the output truncated.

### REQ-TOOL-021 — A command that ends with a non-zero exit status
When a command ends with a non-zero exit status, the sandbox port shall return a normal result carrying that exit status and the captured output.

### REQ-TOOL-022 — A sandbox that cannot run
If a sandbox cannot start or fails on its own, then the sandbox port shall end the call with code `internal`.
