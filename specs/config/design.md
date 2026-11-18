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
layering.yaml`). `layering.yaml` has no line for `internal/config` today, so
`gate layering` fails as soon as the package exists with the current graph:
the statement that the current graph needs no change is not true. Before
WP-L1-05 is implemented, a prerequisite move adds `internal/config: [core]`
to `layering.yaml`, through its own ADR and layering pull request (changing
the graph needs an ADR, AGENTS.md). Until that lands, the implementer does
not create `internal/config`.

The same prerequisite move lets `cmd/kit` and `internal/bdd` import
`internal/config`: the `layers` entry is the only place `internal/gate/layering.go`
reads what a layer may import (it forbids an import only when the imported
layer is neither the importing layer nor in its list), so their existing
lines each gain `internal`, named with the top-level package on purpose.
Every `layers` value names a layer, so no value may say `internal/config`
(that exact key would be a layer that `deny_external` cannot attach to), and
two layers in `internal/` (`internal/bdd`, `internal/gate`) outside a shared
enclosing layer make the whole `internal` directory the only entry that lets
`internal/bdd` reach `internal/config`. As a consequence `cmd/kit`,
`internal/bdd`, `internal/gate` and `tools/gate` (whose list names
`internal/gate`) may each import any nested package under `internal/` that
`layers` does not name, and `internal/gate` itself may now import the
repository's public packages (`core`, `platform`, …). `internal/gate` and
`internal/bdd` stay layers of their own and keep their current lists,
because the longest match wins. Keep untrusted code out of `internal/`: such
a package is importable by those four layers without another graph change.

Keep `internal/config` free of every port implementation and of
`go.temporal.io/`: it returns validated values and lets `cmd/kit` import the
implementations, and it imports `core` only. `cmd/kit` may already import
`platform` and `runtime`; with the prerequisite move it also imports
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
