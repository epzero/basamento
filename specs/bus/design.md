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

- **Cloud open, `OpenCloud(ctx, url, opts)`.** The URL has scheme `nats` or
  `tls`; anything else is refused. Credentials in the URL (a user name, a
  password or a token) travel in clear over `nats://`, so there they are
  accepted only for a loopback host: `localhost`, an address in `127.0.0.0/8`
  or `::1`. Any other host needs `tls://` (REQ-BUS-026). The check runs on the
  parsed URL before the client connects, so a refused URL reaches no network
  and its scenario needs no remote server. A `nats://` URL without
  credentials is accepted on any host: a refusal would not protect a secret
  and an operator may have a private network. A refused URL fails with
  `invalid_argument` and one fixed message, `plaintext credential requires
  loopback or tls`, which holds no part of the URL: the error carries no
  details, and neither the URL nor an error from the client is ever logged or
  returned as is.
- **Embedded open, `OpenEmbedded(ctx, dataDir, opts)`.** The JetStream store
  lives in `dataDir/nats`. Every failure to prepare it (creating the
  directory, reading its mode, starting the server) is a `core` error with a
  fixed message that names no path and holds no text from `nats-server`;
  failures to reach the in-process server, or a context that ends first, use
  the codes of REQ-BUS-013 and the context mapping below. The store directory
  is `0700`: an existing one is narrowed to `0700` before the server starts,
  and a directory the process cannot narrow fails the open with `internal`
  (REQ-BUS-028), rather than letting another local user read cursors and
  locks.

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
  (REQ-BUS-009) and when the buffer overflows (REQ-BUS-010). The subscriber
  resumes with `Subscribe` after the last bus sequence it received.

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
- **Replay is paced, live is not**: a message that already exists when the
  subscription starts (bus sequence at or below the subject's last sequence
  at `Subscribe` time) is a replayed message. Replay waits for room in the
  subscription buffer, so it moves at the subscriber's pace however many
  messages it has to deliver, and it never ends the subscription (REQ-BUS-003).
  A later message is live: it takes a free place in the buffer without
  delaying the publisher, and when the buffer is full it ends the
  subscription after the buffered messages (REQ-BUS-010). No live message
  enters the channel after the subscription's context is cancelled
  (REQ-BUS-009).

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

- **KV value**: one bound covers both a message and a KV value. A KV value is at
  most the maximum message size minus the eight-byte deadline header that
  every KV record starts with: 1 MiB − 8 = 1,048,568 bytes by default. `Put`
  and `CompareAndSwap` refuse a larger value with `invalid_argument` on every
  backend, before writing (REQ-BUS-029). The connection's `max_payload` must
  allow at least the maximum message size plus the header; when it does not,
  the open fails with `invalid_argument` instead of accepting values the
  transport cannot carry. The client's `max_payload` is a second bound, as in
  the message row: a smaller one lowers it.
- **Subject**: one or more dot-separated tokens of letters, digits, `-` and `_`;
  no empty token, no wildcard (`*`, `>`), no whitespace.
- **Key**: ASCII letters and digits and `-`, `_`, `/`, `=`, `.`; 1 to 512
  bytes; dot-separated tokens, none empty, so no leading or trailing `.` and
  no `a..b`; no wildcard, no whitespace. A key is checked by one function
  that the store and the fake share; a refused key fails with
  `invalid_argument` before any storage call (REQ-BUS-012). The grammar is
  narrower than the NATS KV key grammar: letters with a case, accents and
  other scripts are refused, so a key is written with ASCII only.
- The same validation applies to `Subscribe`, `Purge`, `LastSeq` and every KV
  call. Large payloads travel by reference (constitution 7), never on the bus.

## 6. Errors

| Situation | Code |
| --- | --- |
| Invalid subject, key, TTL, or a message over the size limit | `invalid_argument` |
| Key without an entry (or expired) on `Get` | `not_found` |
| Replay from below the purge floor | `not_found`, detail `purged_through` |
| Swap at a revision that is not current; create on an existing entry | `conflict` |
| A purge-floor record that cannot be decoded (shorter than eight bytes) | `internal`, with no key, value or server text: it is never read as *nothing purged* |
| Server unreachable or connection not established | `unavailable`, returned at once: publishes are not buffered while disconnected |

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
- Every subscriber is opened with the buffer its scenario names, on every
  backend (`256` when it names none). A step that publishes a range of
  messages waits, after each publish, until the reading subscribers have
  received it, so a range never depends on speed.
- The step that cancels a subscription waits for its channel to close
  (REQ-BUS-009), then asserts that no message arrives after it.
- BUS-013-01 stops the server of the driver in use and asserts that the next
  calls fail with `unavailable` at once, not after a timeout. For the fake
  this means closing the store's connection, for the embedded implementation
  stopping the in-process server, and for the cloud implementation the
  `nats-server` started for it.
- A stream or bucket the bus cannot use (REQ-BUS-027) is set up on the
  `nats-server` of the cloud row before the open, with a second client:
  `KIT_BUS` is created with a mirror, a source or `Retention: WorkQueuePolicy`
  (the faults that make it incompatible without a mismatch in message
  size), `KIT_KV` by creating the bucket and then changing its backing
  stream the same way. The step checks the configuration again after the
  refused open, so the scenario also shows that a refusal changes nothing.
