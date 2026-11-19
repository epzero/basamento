# otel-log compatibility build

A local copy of `go.opentelemetry.io/otel/log` v1.47.0 with four
backward-compatible helpers and one type alias restored:

| Added | v1.47 spelling |
| --- | --- |
| `log.KeyValue` | `attribute.KeyValue` |
| `log.String` | `attribute.String` |
| `log.Int64` | `attribute.Int64` |
| `log.Bool` | `attribute.Bool` |
| `log.Float64` | `attribute.Float64` |

## Why

`runtime/temporal/embedded` starts `go.temporal.io/server` v1.32.2 in-process
(REQ-RT-003). That release's `common/wideevents` package still uses the
pre-v1 `log.KeyValue` / `log.String` / `log.Int64` helpers, while the root
module's cloud dependencies (`gocloud.dev` v0.49.0, `google.golang.org/api`)
select `go.opentelemetry.io/otel/log` v1.47.0, where they were removed. A Go
module graph has one version per module, so the two cannot otherwise share a
binary; the server does not compile against v1.47.

This module is wired in through a `replace` directive in the root `go.mod`
only. It is a copy of the upstream v1.47.0 package (Apache-2.0, see
`LICENSE` and the file headers) plus the compatibility file. Nothing else
changes; the log API, record model and providers are upstream's.

Remove the `replace` and this directory when the pinned Temporal release
accepts `go.opentelemetry.io/otel/log` v1.x.
