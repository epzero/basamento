# Bus and KV: design notes for the implementer

Requirements: [`requirements.md`](requirements.md). Decisions: ADR-0004 (ports
with embedded and cloud implementations), ADR-0005 (the bus under streaming).

## 1. Packages

| Package | Contents |
| --- | --- |
| `platform` (`platform.go`) | The `Bus` and `KV` ports and `Message`. The changes in §2 are made there. |
| `platform/bus` | The embedded implementation (`nats-server` in-process, JetStream file storage under the data directory) and the cloud implementation (`nats.go` against a NATS cluster). One JetStream code path serves both; they differ in how the connection is made. |
| `platform/bus/bustest` | The in-process fake. Test-only (constitution 11): imported from `_test.go` files and `internal/bdd` only. |

Selecting an implementation by URL (`nats-embedded://…` or `nats://…`) is
wiring, specified with `kit serve` (WP-L1-05), not here.

## 2. Port changes

The contract in `platform/platform.go` predates these specs. Because
`platform/platform.go` is shared across `WP-L1-*` and outside `WP-L1-03`'s
owned directory (`platform/bus`), the implementer lands these port changes
first in a small pull request of their own before the `platform/bus`
implementation (`docs/engineering/agent-workflow.md` §4), noting the touch to
`platform/platform.go` in the pull request description:

- **`Subscribe(ctx, subject, after)`**: `after = 0` means *from the start*
  (every retained message), not live only. Otherwise no full replay is
  possible, because bus sequences start at 1. A live subscription is
  `Subscribe(ctx, subject, LastSeq(subject))`; a message published between
  the two calls is delivered, which is what REQ-BUS-004 allows.
- **`LastSeq(ctx, subject) (uint64, error)`**: the highest bus sequence
  ever assigned on the subject (0 if none), purged or not.
- **`Purge(ctx, subject) error`**: drops the subject's retained messages
  (REQ-BUS-007). The streaming layer calls it when a message's deltas are no
  longer needed; when that is decided by time, the streaming layer owns the
  timer and its clock.
- **`CompareAndSwap(ctx, key, value, revision, ttl)`**: gains a TTL so a
  holder can renew (REQ-BUS-020). `revision = 0` means *create*
  (REQ-BUS-017); `ttl = 0` means no expiry. `Put` keeps its TTL.
- The subscription ends by closing its channel: on cancellation of `ctx`
  (REQ-BUS-009, REQ-BUS-027) and when the buffer overflows (REQ-BUS-010). The
  subscriber resumes with `Subscribe` after the last bus sequence it received.

## 3. Bus sequences and purges

- A bus sequence is the JetStream stream sequence. It increases per subject but
  is not contiguous per subject, so a gap in the numbers is not a lost message.
  The streaming layer orders deltas by its own `dseq`, carried in the data.
- Each subject has a **purge floor**: the last bus sequence purged from it.
  `Subscribe` with `0 < after < floor` fails with `core.CodeNotFound` and the
  detail `purged_through: <floor>` (REQ-BUS-008). `after = 0` and
  `after >= floor` are accepted. The floor survives restarts (REQ-BUS-014).
- Replay then live is one ordered delivery (one JetStream consumer starting at
  `after + 1`), so the seam between replayed and live messages has no gap and
  no duplicate (REQ-BUS-003).

## 4. TTLs follow the injected clock

The NATS server's own message TTLs run on the wall clock, which the gate cannot
control. So the logical expiry of a KV entry is computed against the
`core.Clock` the port is constructed with (the fake clock in the gate, the
system clock in production):

- An entry is stored with its deadline (`time of put, swap or create + ttl`).
  `Get` returns `not_found` once `clock.Now() >= deadline` (REQ-BUS-018).
- A create on a key whose entry has expired replaces it (REQ-BUS-019); a swap
  at the revision of an expired entry fails with `conflict` (REQ-BUS-025).
- The implementation may also set a longer server-side TTL as a backstop for
  cleanup; it never decides expiry.
- In the cloud profile, processes compare deadlines written by other
  processes against their own clocks; TTLs must be much longer than the clock
  skew between hosts.

## 5. Limits and validation

| Setting | Default | Requirement |
| --- | --- | --- |
| Maximum message size | 1 MiB (also bounded by the server's `max_payload`) | REQ-BUS-011 |
| Subscription buffer | 256 messages | REQ-BUS-010 |

- **Subject**: one or more dot-separated tokens of letters, digits, `-` and `_`;
  no empty token, no wildcard (`*`, `>`), no whitespace.
- **Key**: letters, digits, `-`, `_`, `/`, `=` and `.`; not empty, not starting
  or ending with `.`; no wildcard, no whitespace (the NATS KV key rules).
- The same validation applies to `Subscribe`, `Purge`, `LastSeq` and every KV
  call. Large payloads travel by reference (constitution 7), never on the bus.

## 6. Errors

| Situation | Code |
| --- | --- |
| Invalid subject, key, TTL, or a message over the size limit | `invalid_argument` |
| Key without an entry (or expired) on `Get` | `not_found` |
| Replay from below the purge floor | `not_found`, detail `purged_through` |
| Swap at a revision that is not current; create on an existing entry | `conflict` |
| Server unreachable or connection not established | `unavailable`, returned at once: publishes are not buffered while disconnected |
| The context of the call is cancelled | `cancelled` |
| The context of the call has passed its deadline | `timeout` |
| A KeyValue entry cannot be decoded as a KV entry | `internal`, with no key, value or server text in the error |

- **A message too large for the server** (JetStream API error 10054, or the
  client's maximum payload) is a size limit like any other:
  `invalid_argument` (REQ-BUS-011).
- **A corrupt KV record** (REQ-BUS-026) is a record under a key that the store
  cannot decode, for example one written by another client. Reads, swaps and
  creates of that key fail with `internal` before any expiry or revision check.
  A put over the key replaces the record, so a later read succeeds.
- **Cancellation and deadlines** (REQ-BUS-027) are checked on every call of
  `Publish`, `LastSeq`, `Purge`, the setup of `Subscribe` and every KV call: a
  context that is already cancelled or past its deadline fails the call before
  the store does anything, and a context that ends while a call waits on the
  server fails that call the same way. When the context of an open
  subscription ends, the subscription ends by closing its channel; it is not
  an error of the call that opened it.

## 7. How the scenarios run

- The component driver runs every scenario of `publish-and-replay.feature`,
  `retention.feature`, `limits-and-failures.feature`, `kv.feature` and
  `ttl.feature` against the fake, the embedded implementation and the cloud
  implementation. The cloud implementation is reached through a `nats://` URL
  served by a `nats-server` started in the test process, so the gate needs no
  external service. The restart scenarios of `embedded.feature`, tagged only
  `@profile:embedded`, run against the embedded implementation alone.
- "Receives … and nothing else" and "receives nothing" are asserted without
  sleeps: after the expected messages, the driver publishes a sentinel and
  checks that it is the next message delivered.
- Time moves only through the fake clock (`core/coretest`).
- The corrupt-record scenario (BUS-026-01) runs against the embedded and the
  cloud implementations, which keep the KV entries in a JetStream bucket,
  and not against the fake, whose store holds decoded entries and cannot hold
  a foreign record. The driver writes the foreign record into the bucket
  named in §1 (`KIT_KV`) through a test-only helper of
  `platform/bus/bustest` that connects to the same server and writes the
  value under the key through `nats.go` and JetStream directly, as any other
  client of the bucket would. The value is shorter than a KV record header,
  so the store cannot decode it.
- The cancellation and deadline scenarios (BUS-027-01 and BUS-027-02) pass a
  context that is already cancelled, or whose deadline is set to a time
  before the frozen clock, to the driver; neither needs real time. The
  driver asserts that nothing was stored by reading `cursor.r1` and replaying
  `run.r1.deltas` from the start with an uncancelled context: the read fails
  with `not_found` and the replay receives no message.
