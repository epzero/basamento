# Config: design

**Status:** draft, WP-L1-05, spec review pending.

Decisions the implementer of WP-L1-05 must follow. They refine the
requirements; where this file and the requirements disagree, the requirements
win and this file is fixed in the same change.

ADR-0004 fixes the mechanism: koanf, plain constructors (`wireEmbedded`,
`wireCloud`), no dependency-injection code generator, no reflection registry.

## 1. Packages

| Package | Role | Owned by |
| --- | --- | --- |
| `internal/config` | load, validate, effective report, schema of known keys | WP-L1-05 |
| `structs.go` in `internal/config` | the `Config` struct with `koanf` tags | WP-L1-05 |
| `cmd/kit` | `serve` and `config validate` commands, start-up order, signals | WP-L1-05 |
| `platform`, `platform/sql`, `platform/blob`, `platform/bus`, `platform/secrets` | the ports and their constructors | WP-L1-01 … 04 |

`internal/gate/layering.go` fails the gate for every package `layering.yaml`
does not assign to a layer (`package not assigned to a layer in
layering.yaml`). ADR-0016 added the row, and `layering.yaml` on `main` has it
before WP-L1-05 starts:

```yaml
internal/config: [core]
```

The graph therefore admits the package: the implementer creates
`internal/config` under that row and changes nothing in `layering.yaml`
(changing the graph needs an ADR, AGENTS.md). The row allows imports of
`core` only: no `platform`, no `runtime`, no port package.

`cmd/kit` and `internal/bdd` are the only layers that import `internal/config`,
each through a path-segment prefix entry naming that package, not the
`internal` directory. `internal/gate/layering.go` matches a `layers` value on
whole path segments and keeps the longest match, so the value
`internal/config` grants that tree alone (`internal/bdd/steps` included,
through its layer), while the value `internal` would grant every nested
package under `internal/` that `layers` does not name. The broad entry is not
merely wider than needed, it is wrong for this graph: with `internal` in
`cmd/kit`'s list, `cmd/kit` may import `internal/gate` and `gate layering`
stays green, so the gate binary joins the shipped graph and the two-importer
rule is void. Naming `internal/config` in a list is valid: it is a layer key
of its own, so `deny_external` attaches to it, and in another layer's list it
is a path-segment prefix.

The narrow grant keeps the graph closed. Exactly two layers import
`internal/config`: `cmd/kit`, for `Load` and `Validate`, and `internal/bdd`
and its `steps`, for the harness. `internal/gate` (empty list) and
`tools/gate` (whose list names `internal/gate`) import no configuration and
stay closed. Whether code under `internal/` is trusted is a policy, not a
gate check: by policy untrusted code does not live there, the gate cannot
tell it apart, and a new `internal/<pkg>` row or grant needs an ADR
(ADR-0016).

`deny_external` for `internal/config` lists the port vendor prefixes:
`go.temporal.io/`, `gocloud.dev/`, `github.com/jackc/pgx/`, `modernc.org/`,
`github.com/nats-io/`, `github.com/tink-crypto/`, `github.com/aws/`,
`cloud.google.com/`, `github.com/Azure/` (ADR-0002, ADR-0004). The gate has
no way to know which modules are port vendors, so whoever adds a port library
to the config package extends that list in the same change and a reviewer
checks it; an import the list does not name passes. Imports are read from
`go list` in the gate's build context (Linux on CI), so a file built only on
another platform is outside the check and is reviewed by hand; `_test.go`
imports are not read at all, as `test_only` already relies on. The standard
library and small parsers are allowed.

Keep `internal/config` free of every port implementation and of
`go.temporal.io/`: the allow-list admits `core` only and `deny_external`
blocks the vendors above. It returns validated values and the implementation
name a URL selects (REQ-CONFIG-011, REQ-CONFIG-012), and lets `cmd/kit`
import the implementations and their constructors. `cmd/kit` may already
import `platform` and `runtime`; with the row in place it also imports
`internal/config`.

## 2. Keys, types and defaults

One table in `structs.go` is the single source for the kebab-case key, the Go
field, the default and the validator. It is what `kit config validate`
renders and what the environment mapping is derived from.

| Key | Type | Default | Rule |
| --- | --- | --- | --- |
| `profile` | string | `embedded` | one of `embedded`, `cloud` |
| `listen.addr` | host:port | `127.0.0.1:8080` | parses, port > 0 |
| `data.dir` | path | `./.kit/data` | non-empty |
| `log.level` | string | `info` | one of `debug`, `info`, `warn`, `error` |
| `log.format` | string | `text` | one of `text`, `json` |
| `sql.url` | URL | profile-dependent (below) | scheme per profile |
| `blob.url` | URL | profile-dependent | scheme per profile |
| `bus.url` | URL | profile-dependent | scheme per profile |
| `secrets.url` | URL | profile-dependent | scheme per profile |
| `shutdown.timeout` | duration | `10s` | > 0, < 5m |

The embedded profile defaults the four port URLs to
`sqlite://$(data.dir)/kit.db`, `file://$(data.dir)/blobs`,
`nats-embedded://$(data.dir)/nats`, `localsecrets://$(data.dir)/secrets.key`.
`$(data.dir)` is expanded after loading, so changing `data.dir` moves every
default port URL with it. The cloud profile has no default for a port URL:
each must be set (REQ-CONFIG-010).

## 3. Loading

```go
package config

// Result is an effective configuration and the source of each key.
type Result struct {
    Config  Config
    Sources map[string]Source // key -> flag, env, file, default
}

// Load reads the sources in precedence order and returns the effective
// configuration. It returns an error only when a configuration file cannot
// be read or parsed.
func Load(opts Options) (Result, error)

// Validate checks every setting rule (including URL syntax, REQ-CONFIG-017)
// and returns every error, one per offending key, in key order.
func (r Result) Validate() []error

// Report renders the effective configuration with its sources; any password
// in a URL setting prints as *** (REQ-CONFIG-008).
func (r Result) Report(w io.Writer)

type Options struct {
    File   string              // configuration file path; empty = none
    Env    []string            // os.Environ() form
    Stdin  io.Reader           // for YAML from stdin ("-"), optional
    Flags  map[string]string   // flags actually set on the command line
}
```

- **File.** `--config <path>`; YAML. Without the flag, the config file a
  command declared defaults to is used when present (`./kit.yaml` for
  `serve`). A missing file is not an error unless `--config` names it.
- **Environment.** Every `KIT_<KEY>` with `.` written as `_` and letters
  uppercased (REQ-CONFIG-002). The mapping is built from the key table, so
  `KIT_LISTEN_ADDR` is `listen.addr`; an unknown `KIT_` variable is ignored.
- **Flags.** Only flags the operator actually typed count; a flag at its
  zero value does not override the environment. Flags are kebab-case dotted
  names: `--profile`, `--listen.addr`, `--data.dir`, `--log.level`,
  `--shutdown.timeout`, `--config`.
- **Durations** are Go duration strings. **Sources** are recorded per key in
  `Result.Sources`.

Validation errors are `*core.Error` with code `invalid_argument`, a message
naming the key (`invalid setting "bus.url": scheme "amqp" is not accepted by
profile "embedded"`) and no part of the value other than the scheme of a
URL that parsed. An error never shows the URL's userinfo, host, port, query
or path, the raw text of an unparseable `net/url` error (which quotes the
input), or any part of a value that is not a URL (`profile`,
`log.level`, …): for those only the key and the rule are named
(REQ-CONFIG-007). Errors carry `Details{"key": key}` for the report.

## 4. Selection

```go
package config

// Port names, used in errors and readiness reports.
const (PortSQL, PortBlob, PortBus, PortSecrets = "sql", "blob", "bus", "secrets")

// Implementation returns the implementation a port URL selects, by scheme.
func (c Config) Implementation(port string) string
```

The accepted schemes and the implementation names per scheme are REQ-CONFIG-011
and REQ-CONFIG-012 (`gs` → `gcs`; every other scheme keeps its name). The
profile narrows the list: an embedded profile rejects `postgres` even though
the binary contains it. `Implementation` returns the selected name for a
validated URL and is never called on an unvalidated one.

## 5. `kit serve` start-up

1. `config.Load`, then `Validate`. Any error: print every error to stderr,
   exit 1. Nothing is opened (REQ-CONFIG-005).
2. WP-L1-05 does not start the temporal runtime. The runtime the profile
   selects is temporal, the default for both profiles, in-process for
   `embedded` (REQ-RT-003), and configuration has no runtime setting; but
   WP-L5-02 and WP-L5-03 own `runtime/temporal` and
   `runtime/temporal/embedded` and depend on WP-L1-05, so WP-L1-05 must not
   wait for them. WP-L1-05 writes the start-up order with the step where the
   runtime starts at its place, selected by profile and named in the `ready`
   line as below, opening and closing nothing itself. WP-L5-02 and WP-L5-03
   wire starting the temporal runtime there into `kit serve`, their own
   scenario `RT-003-01` proving it; until then that step is a no-op and
   `kit serve` opens the ports and binds its listener without a runtime
   (REQ-CONFIG-020 and the `ready` line stay one requirement, met when the
   line is printed once the ports are open and the listener is bound).
   Then open the ports in the order sql, blob, bus, secrets. Each `Open`
   takes a `context.Context`. An error names what failed the same way: close
   what is already open in reverse order, print `port "sql" could not open:
   …`, exit 1 (REQ-CONFIG-021).
3. Bind the listener. On error, close everything, report `listen.addr`, exit
   1 (REQ-CONFIG-022).
4. Serve, and print one `ready` line on stdout once everything above
   succeeded: profile, runtime, listener and each port's implementation. It
   is the observable "ready" of the scenarios (REQ-CONFIG-020). `GET
   /healthz` returns `200` from the moment the listener is bound.

`serve` waits for a termination signal (`SIGTERM`, `SIGINT`). Shutdown: the
listener stops accepting, requests in flight on it get `shutdown.timeout`,
then every port closes regardless and the process exits 0 if all finished in
time and 1 otherwise (REQ-CONFIG-023, REQ-CONFIG-024).

## 6. Test harness

- Component scenarios call `config.Load`, `Result.Validate`, `Result.Report`
  and the server lifecycle runner in-process. `CONFIG-023-01` and
  `CONFIG-024-01` drive the in-process server runner with the
  `core/coretest` fake clock and an in-flight request controlled by the
  component driver.
- System scenarios run the built `kit` binary (`kit serve` and `kit config
  validate`) and observe only its stdout `ready` line, stderr messages,
  `GET /healthz`, `SIGTERM` and process exit status.
- In `CONFIG-021-01` ("a data directory that cannot be written"), the system
  driver points `data.dir` at a path occupied by a regular file so that
  opening the first port (`sql`) fails deterministically without any test
  hook or vendor call. In `CONFIG-022-01`, the test holds `127.0.0.1:8080`
  with a loopback listener of its own before starting `kit serve`.

## Open questions

- **Secret keys outside URL userinfo**: In WP-L1-05, every secret in configuration is a URL password (`REQ-CONFIG-008`). When `WP-L8-01` (`specs/auth`) introduces non-URL secret settings (if any are not stored in the `Credentials` vault), should key-name masking be specified in `specs/auth` at that time?
- **Temporal runtime schemes**: Deferred to `WP-L5-02` and `WP-L5-03`, which own the Temporal runtime and embedded Temporal server.
