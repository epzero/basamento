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

`internal/config` is `internal/`: `layering.yaml` has no line for it, so the
layering gate enforces nothing about its imports, and no ADR covers it yet.
Keep it free of every port implementation and of `go.temporal.io/`: it
returns validated values and lets `cmd/kit` import the implementations. If
the implementer wants a rule for it, that is a change to `layering.yaml`,
which needs an ADR and is not part of this work package. `cmd/kit` already
may import `platform` and `internal/config` will import `core` only, so the
existing graph needs no change.

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
| `runtime.url` | URL | empty | empty, `local://`, `temporal-embedded://…`, `temporal://…` |
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
// configuration. It returns *core.Error{Code: invalid_argument} when a URL
// cannot be parsed (REQ-CONFIG-017); it does not validate further.
func Load(opts Options) (Result, error)

// Validate returns every error, one per offending key, in key order.
func (r Result) Validate() []error

// Report renders the effective configuration with its sources; secret keys
// (key contains secret, password or token) print as ***.
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
profile "embedded"`) and no part of the value. A value that is itself a URL
is reported by scheme only, never with its userinfo, query or path
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
2. Select the runtime and start it (embedded Temporal server when asked),
   then open the ports in the order sql, blob, bus, secrets. Each `Open`
   takes a `context.Context`. An error names what failed the same way: close
   what is already open in reverse order, print `port "sql" could not open:
   …` (or `runtime "temporal-embedded" could not start: …`), exit 1
   (REQ-CONFIG-021).
3. Bind the listener. On error, close everything, report `listen.addr`, exit
   1 (REQ-CONFIG-022).
4. Serve, and print one `ready` line on stdout once everything above
   succeeded: profile, runtime, listener and each port's implementation. It
   is the observable "ready" of the scenarios (REQ-CONFIG-020). `GET
   /healthz` returns `200` from the moment the listener is bound.

**The out-of-process test hook.** The system driver must observe the ports
that `kit serve` holds and an in-flight request that it can make finish or
slow down, on a binary that, until WP-L6-03, serves only `/healthz`. `kit
serve` therefore mounts a `net/http/pprof` handler on the same listener,
guarded by `KIT_DEBUG=1`, added in `cmd/kit/debug_ports.go` to every build
(CGO stays off and the binary stays static; the routes return 404 unless the
variable is set at start).
It provides:

- `GET /debug/pprof/ports`: JSON `{"sql":"open","blob":"open",…}`.
- `POST /debug/pprof/hold?ms=1000|10000`: an HTTP handler that blocks for
  `ms` of real time before responding. This is what "a request in flight"
  means in CONFIG-023-01 and CONFIG-024-01.

It is present in production builds, behind the variable, and contains no
fake port: the ports it lists are the real ones. To be weighed at review:
if the owner prefers it out of the binary, the alternative is to land
WP-L6-03's `config` connect service first and drive the scenarios through it,
which makes WP-L1-05 wait on WP-L6-03.

**Do not use `KIT_DEBUG` in a scenario:** the probe is started by the
system driver before the scenario's steps, never by a step.

`serve` waits for a termination signal (`SIGTERM`, `SIGINT`); `kit config
validate` does not install a handler, so the same signal kills it during its
work (that is the "in flight" of CONFIG-023-01). Shutdown: the listener
stops accepting, requests in flight on it get `shutdown.timeout`, then every
port closes regardless and the process exits 0 if all finished in time and 1
otherwise (REQ-CONFIG-023, REQ-CONFIG-024). The ports close only after the
in-flight request has finished or the timeout has passed, whichever is first;
that is what "closes its sql port at 1 second" and "at 5 seconds" check.

## 6. Test harness and points that touch other work packages

- Component scenarios call `config.Load` and `Result.Validate` in process.
- System scenarios run the built binary. CONFIG-021-01 needs a port whose
  `Open` fails without a vendor: the test starts the real binary with
  `--config` naming `sql.url: sql://prod`, and the system driver registers a
  `sql` implementation for that scheme that refuses to open. The other
  three ports are the binary's real embedded ones. Nothing reaches a
  vendor, and the bind failure in CONFIG-022-01 holds the listener with a
  socket of the test, so both are deterministic.
- Timeouts: in-process uses the `core/coretest` fake clock; the
  out-of-process binary uses the real timer with short durations
  (`shutdown.timeout` `5s`, a request that takes 1 s and one that takes
  10 s). No test sleeps waiting for an event; it waits on the process.
- The `@level:system` scenarios use only the `ready` line, `/healthz`,
  signals and exit status, so they do not wait on WP-L6-03. They do not call
  the connect API; the G6 smoke run drives the binary's existing surface.
- **Lands in WP-L8-01:** the `auth.*` keys the report masks
  (CONFIG-008-01) are declared by that work package; until then the schema
  table has no `auth.*` key and the loader ignores unknown file keys.
- **Lands in WP-L5-03:** the `temporal-embedded` runtime constructor. This
  work package validates the scheme, calls the constructor before it opens
  the ports' `ready` line, and fails start-up naming `runtime` if the
  constructor errors; CONFIG-026-01 activates with that work package.
