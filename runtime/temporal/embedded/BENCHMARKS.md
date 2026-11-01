# Embedded Temporal server: benchmarks

Measurements of the in-process Temporal server used by the embedded profile
(single-user daily use), and the reasons behind its settings. Re-run them after
any change to the server version or the settings.

Environment: Linux x86-64, 4 vCPU, Go 1.26.10, `go.temporal.io/server` v1.32.2,
`go.temporal.io/sdk` v1.47.0, `go.temporal.io/api` v1.63.5, `modernc.org/sqlite`
v1.51.0, built with `CGO_ENABLED=0`.

```bash
cd runtime/temporal/embedded
go build -o bench-cli ./bench
./bench-cli perf  -dir "$(mktemp -d)"         # add -temporal-defaults to compare with Temporal's settings
./bench-cli crash -dir "$(mktemp -d)"         # kill -9 mid-run, restart, verify recovery and replay
./bench-cli perf  -hostport 127.0.0.1:7233    # an external server, e.g. `temporal server start-dev`
```

## Results

| Metric | Result |
| --- | --- |
| Binary size (server + SDK + worker, stripped, `CGO_ENABLED=0`) | 116 MB |
| Cold start, fresh database (schema + namespace + services) | 0.3 – 0.55 s |
| Start with an existing database | 64 – 95 ms |
| Peak memory (RSS) | 205 – 218 MB |
| One-activity workflow, start to result | p50 100 ms, p99 106 ms |
| Sequential activities | ~50 ms each |
| 200 parallel activities | 0.43 – 0.75 s (1.3 – 1.6 s with Temporal's default of four task-queue partitions) |
| 20 concurrent workflows × 20 activities | 1.3 – 2.7 s |
| 2 s durable timer | fired after 2.06 s |
| Validated Update (gate) | invalid payload rejected synchronously; valid one accepted in 5 – 15 ms |
| Continue-as-new | 1,000 activities in batches of 500: 49 s |
| Database growth | 3.7 – 5.8 MiB per ~4,100 activities |
| Crash recovery (`kill -9` mid-run, restart) | correct in every run: all steps completed, completed steps never re-ran, at most the in-flight step re-ran |
| Restart to completion | 5 – 8 s with a 2 s workflow-task timeout (includes ~4 s of remaining work); up to 15 s with Temporal's 10 s default |
| Replay | the recorded history replays with the same code; changed code is detected as nondeterminism |

## Why the settings are what they are

1. **About 50 ms per sequential step is Temporal's own overhead.** The time
   sits between `WorkflowTaskScheduled` and `WorkflowTaskStarted` (~43 ms), and
   the official `temporal server start-dev` built from the same versions shows
   the same number, also with an in-memory database. A 20-step agent loop adds
   about 1 s, small next to model latency. Interactive work that needs lower
   latency runs on the `local` runtime.
2. **One task-queue partition** (`matching.numTaskqueueReadPartitions` and
   `…WritePartitions` = 1). With a single worker process, more partitions only
   add forwarding latency; fan-out is 2–3× faster.
3. **SQLite: `journal_mode=wal`, `synchronous=normal`, `busy_timeout=10000`, four
   connections.** These are plain pragma names in `ConnectAttributes`. The
   settings double fan-out throughput and survive a process crash. A power loss
   may drop the last commits, which steps tolerate because they are
   at-least-once and idempotent.
4. **A short workflow-task timeout (2–3 s)** set by the runtime when it starts
   workflows. A workflow task handed to a process that died is otherwise retried
   only after Temporal's 10 s default.
5. **Activities are at-least-once.** A step that finished its side effect but
   had not reported completion before a crash runs again, so every step is
   idempotent per `IdempotencyKey`.
6. **Pin `go.temporal.io/api` and `go.temporal.io/sdk` to the versions the
   server release was built with** (from the matching `temporalio/cli` release).
   Newer API modules do not compile against a given server release.
7. **Go ≥ 1.26.10** is required by server v1.32.
8. **The server's dependencies are heavy.** That is acceptable in the `kit`
   binary but must stay out of library packages, so this is its own Go module
   (ADR-0002).
