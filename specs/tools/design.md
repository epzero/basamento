# Design: the tool layer

How the implementer builds `specs/tools`. The requirements say *what*; this
file fixes the interfaces the rest of the repository programs against and the
decisions the architecture docs leave open. Where it and a requirement
disagree, the requirement wins and this file is corrected.

## 1. Packages

- **`tool`** (owned by WP-L7-02) holds the port, the registry and allowlists,
  the call step, the MCP client and the sandbox port. It may import `core` and
  `platform` (`layering.yaml`); the MCP Go SDK
  (`github.com/modelcontextprotocol/go-sdk`) is a third-party module, which
  the layering rule does not restrict. `go.mod` gains the SDK here; `gate
  layering` checks package imports, not modules. WP-L7-02 adds no sandbox
  driver module: the gate exercises the port against the simulated sandbox,
  and concrete drivers are a follow-up work package.
- **`agent`** (WP-L7-01, not touched here) owns the loop: it passes the
  allowlist it was given and builds the model's error message (REQ-TOOL-011).
  WP-L7-02 depends on WP-L7-01 (`docs/roadmap/L7-agents.md`), and `agent`
  imports `tool` (`layering.yaml`).
- The BDD harness passes the agent and flow adapters through a seam, so these
  scenarios run before `agent` exists.
- The **simulated MCP server** (an `mcp` server built on the SDK's in-memory
  transport) and the **simulated sandbox** are test helpers in
  `internal/bdd` or a `_test.go` file. They are not `core/coretest`, which
  `layering.yaml` restricts further; no production package imports them.
- `core/schema` (WP-L2-02) validates a call's arguments (REQ-TOOL-002); this
  package does not implement JSON Schema itself.

### Not in `tool`

A `Tool` node handler needs `runtime.DurableContext`, and `tool` may not import
`runtime` (ADR-0011: "A `tool` or `task` node is one step, registered with the
runtime"). The node handler and its registration therefore land where the
composition runs the node, outside `tool`. The step function it registers
calls `tool.RunStep` (§2); nothing about the scenarios needs its own spec
here, they call tools through `invoke`.

## 2. The port

```go
package tool

// Outcome.Text renders what a model sees for a failed call (REQ-TOOL-011):
// the code and the message, never Details.
func (o Outcome) Text() string

// Tool is what one caller may call.
type Tool struct {
    Name        string          // as the model sees it; from an MCP server, mcp_<server>__<tool>
    Description string
    Input       json.RawMessage // JSON Schema
    Timeout     time.Duration   // 0 = the layer's default
    MaxResult   int             // bytes kept before the result is stored by reference; 0 = default
    Run         Func
}

// Func runs the tool. Its error carries the core code and message a model
// sees (REQ-TOOL-004, REQ-TOOL-011); its Details never reach a model.
type Func func(ctx context.Context, args json.RawMessage) (Result, error)

type Result struct {
    Content    json.RawMessage // or a JSON string, for a tool whose result is text
    BlobRef    string          // set instead of Content when the result exceeded MaxResult
    ExitStatus int             // set by a sandbox command (REQ-TOOL-021)
    Truncated  bool            // set by a sandbox result whose output was cut (REQ-TOOL-020)
}

// Set is one caller's tools: the registry names an agent was given.
type Set struct {
    Tools []Tool // sorted by Name, so replay is deterministic
}

// Call is one tool call as the loop or the node received it.
type Call struct {
    Name string
    Args json.RawMessage
}

// Outcome is what a caller gets back: one of Result, or Code and Message.
type Outcome struct {
    Result  Result
    Code    core.Code
    Message string
    Failed  bool
}

// RunStep is the side effect of one call, run by a step named by the caller:
// dc.Step("tool:"+call.Name, call, StepOptions{IdempotencyKey: Key(...)}).
// It validates, checks the allowlist, runs the tool under its limits and
// returns an Outcome; it returns a Go error only when the layer itself failed.
func RunStep(ctx context.Context, set *Set, call Call, now func() time.Time) (Outcome, error)
```

The composition root builds a `Layer` and passes its `Step` method as
`agent.Options.RunTool`; `RunStep` keeps the signature above for a caller with
no blob store. The layer's dependencies are a `Clock` (`Now` and `After`), a
`platform.Blob` for oversized results, a `platform.Credentials` for MCP
servers, and a `*slog.Logger` for panic values and driver failures. `After`
comes from the injected clock so a timeout fires on the fake clock in the
gate, never on wall time.

## 3. Decisions

**Step key and result (REQ-TOOL-003).** The key is
`tool:<run>:<node path>:<visit>:<call index>`; the node path is the same one
the model node uses (map index and enclosing subflows included), `visit`
counts visits to that node or turns of the agent loop, and `call index`
counts the calls within that visit or model reply in order. The step returns
the `Outcome`, success or failure, so a replay returns the recorded outcome
and never the tool. Every outcome is recorded, including `permission_denied`
and the two refusals below.

**Order inside the step.** Allowlist check (REQ-TOOL-008) → unknown name
(`not_found`; the allowlist check comes first, so a tool an agent was not
given is always `permission_denied`) → schema validation (REQ-TOOL-002) →
run with the time limit and a heartbeat every second (REQ-TOOL-006) → size check
(REQ-TOOL-007).

**Codes.** REQ-TOOL-004: the tool's own `core.Code` and message. REQ-TOOL-005:
a recovered panic is `internal`, message naming the tool, the panic value
going to the log only. No tool outcome (REQ-TOOL-004, REQ-TOOL-005, the two
refusals above) is a runaway attempt: `RunStep` returns it as an `Outcome`, so
the step succeeds and the runtime never retries it.

**Size limit (REQ-TOOL-007).** The default is **256 KiB**; the layer stores
anything larger with `platform.Blob` and returns `BlobRef`. "A blob reference
and not the result's bytes" is what the scenario checks.

**Error to the model (REQ-TOOL-011).** The loop renders `Code` and `Message`
only. Details, causes and panic values are kept in the log, never in the
message.

**Allowlists (REQ-TOOL-008, REQ-TOOL-009, REQ-TOOL-023).** A `Set` is read
when a model turn is built, not cached across turns, and only its tools are
offered to the model. A call to a tool not in `Set` returns `Outcome{Failed:
true, Code: core.CodePermissionDenied}` without running the tool.

**MCP client.**
- A server is configured by name, an endpoint and an optional credential
  reference `cred://<name>`. The client is the SDK's, with the SDK's in-memory
  transport in tests.
- **Names (REQ-TOOL-012, REQ-TOOL-024):** `mcp_<server>__<tool>`, where the
  server name has any character outside `[A-Za-z0-9]` replaced by `_`. A tool
  name that collides with one already in the caller's `Set` is rejected with
  `conflict` when the server's tools are listed.
- Tool list: re-read before every offer, so a server that changed its tools is
  seen by the next model turn (REQ-TOOL-016). This is the "re-read on every
  model turn" option below; a cache keyed on the SDK's notification would race
  the gate's clock.
- **Unreachable (REQ-TOOL-014):** no session, or the connection fails before
  the call is sent. **Dropped mid-call (REQ-TOOL-015):** the connection fails
  after the call was sent. Both are `unavailable`; neither is retried: a call
  may have had effects, and the platform never decides to pay a second time
  (ADR-0012 §7 treats tools as not restart-safe). The flow's own retry policy
  decides.
- **Errors the server returns (REQ-TOOL-025):** mapped to `vendor_error`, with the server's
  message (a tool error is not a platform failure). A result the server marks
  as an error (`isError`) is `vendor_error` too, its content in the message up
  to the size limit.
- **Credential (REQ-TOOL-017):** resolved with `platform.Credentials` from
  `cred://<name>` when the call is made, passed to the server and dropped. The
  endpoint never carries it; error messages from the SDK that echo headers are
  redacted before they become `core.Error` (`Details` is redacted before it
  leaves the process, `core/errors.go`).
- **Schema:** the server's declared input schema; `additionalProperties`
  follows the server. The layer adds nothing.

**Sandbox port.**

```go
type SandboxSpec struct {
    Image     string          // or a driver-specific base
    TimeLimit time.Duration
    MaxOutput int             // bytes kept in total; 0 = 1 MiB
    Network   []string        // hosts or CIDRs; empty = no network
    Files     map[string][]byte // the only files the command sees, under its work dir
    CredRefs  map[string]string // env var name -> cred:// name, resolved at the run
}
type Sandbox interface {
    Run(ctx context.Context, spec SandboxSpec, command []string) (SandboxResult, error)
}
type SandboxResult struct {
    ExitStatus int
    Stdout, Stderr []byte
    Truncated  bool
}
```

- REQ-TOOL-018: a driver gives the command no host files (only `Files`, in a
  fresh work directory), no host environment (`HOME` is set to the work
  directory, which is what `echo $HOME` shows), no instance metadata address
  and, with `Network` empty, no network. A host file path in a command is
  not rewritten; it simply does not exist. WP-L7-02 implements only this
  port and the simulated sandbox: the gate proves the port's contract
  against it. A concrete driver and its isolation `@live` check are a
  follow-up work package.
- Time (REQ-TOOL-019) and output (REQ-TOOL-020) limits are enforced by the
  layer around the driver as well as by the driver, so the caller always sees
  the same outcome: `timeout` at the limit on the fake clock, the first
  `MaxOutput` bytes and `Truncated`. `Layer.RunSandbox(ctx, driver, spec,
  command)` is the entry point; it takes its timers from the layer's `Clock`.
- Non-zero exit (REQ-TOOL-021): a command that finishes with a non-zero exit
  status returns a normal `Outcome{Failed: false}` whose `Result` carries
  `ExitStatus` and the captured output (`Content`), because a non-zero command
  exit (such as a failing test run) is a tool result, not a tool failure.
- A driver that cannot start (REQ-TOOL-022) returns `internal`.
- The simulated sandbox lives in `internal/bdd`; WP-L7-02 adds no concrete
  driver. The first concrete driver (a `platform`-backed one) and its
  `@live` check are a follow-up work package.

## 4. What the implementer changes outside `tool`

- Nothing, to run these scenarios: the harness seam in `internal/bdd` is the
  harness's own.
- The `tool` node handler and the model-error rendering are WP-L7-01 / the
  flow engine's; they land in their own work packages, which follow §2 and
  §3. If either needs a change in `core/schema` or `prompt`, that change
  lands first, in its own pull request, in that package's owned directory.

## Open questions

1. **Audit events for refused tool calls:** Should a refused tool call (`permission_denied` when an agent or flow calls a tool outside its allowlist) emit an audit event once `WP-L8-02` defines the audit sink, or are audit events restricted to principal authorization denials (`REQ-AUTH-004`), credential resolutions (`ADR-0004`), and worker pool operations (`ADR-0012`)?
2. **Untrusted-input wrapping of tool results (`WP-L7-01` / `WP-L3-05`):** `tool` cannot import `prompt` (`layering.yaml`), so `REQ-TOOL-010` was removed from `specs/tools`. Should the requirement and scenario that tool results returned to a model are wrapped via `prompt`'s untrusted-input wrapper be added to `specs/agent` (`WP-L7-01`)?
3. **Consumer wiring between `agent` (`WP-L7-01`) and `tool` (`WP-L7-02`):** `docs/roadmap/L7-agents.md` states `WP-L7-02` `Depends: WP-L7-01`, while `layering.yaml` has `agent` importing `tool`. When `WP-L7-02` lands `tool`, a small follow-up change in `agent` (or a contract-first PR) will be needed if `agent` was built against a narrow interface before `WP-L7-02`.
