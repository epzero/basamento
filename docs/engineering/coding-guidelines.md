# Coding guidelines

How code, comments and docs are written in this repository. Code reviewers
check every change against this page. Tools enforce what they can
(`gofmt`, `go vet`, `golangci-lint`, `gate layering`); the rest is judgment.

## 1. Comments

- **Say why, not what.** The code already says what it does. A comment explains
  a reason the reader cannot see: a constraint, a trade-off, a vendor quirk, an
  invariant, a non-obvious consequence.

  ```go
  // Bad: increments the counter.
  n++

  // Good: Temporal delivers cancellation only through heartbeats, so long
  // steps must heartbeat at least every few seconds.
  activity.RecordHeartbeat(ctx, progress)
  ```

- **Keep them few and short.** One or two sentences. If a block needs a long
  explanation, the design probably belongs in a doc or the code needs a
  clearer shape.
- **Keep them true.** A change that alters behavior updates or deletes the
  comments around it in the same change. A wrong comment is worse than none.
- **Doc comments** on every exported identifier and every package (`doc.go`).
  Go style: start with the identifier's name and describe the contract
  (inputs, outputs, errors, concurrency, side effects), not the implementation.
- **No history in comments or docs.** No "changed from …", "previously …",
  "new:", dates or author names. History lives in git. Comments and docs
  describe the code as it is.
- **No decision provenance.** Documents state the system as it is and why,
  never who decided or asked for it, when, or in which ticket. That belongs in
  commits, pull requests and the tracker.
- **No commented-out code.** Delete it; git remembers.
- **TODOs carry a work package id or an issue key**: `// TODO(WP-L3-04): …`.
  A TODO without one fails review.
- Reference specs and decisions by id where they explain behavior
  (`REQ-MODEL-005`, `ADR-0006`), so readers can find the reason.

## 2. Go

- **Small packages with one purpose**, placed by `layering.yaml`. Packages are
  imported by path (`github.com/epzero/basamento/flow`); there is no `pkg/` prefix.
- **Interfaces belong to the consumer** and stay small. Return concrete types.
- **`context.Context` comes first** in any function that does I/O, waits or can
  be cancelled. Do not store a context in a struct unless the contract requires
  that type to own the context and its cancel for a lifetime longer than the
  call that created it, as `model.Run` does for an invocation (ADR-0005).
- **Errors:**
  - Return `*core.Error` with a code from `core` at package boundaries.
  - Wrap with `%w`, and add context once per layer.
  - Never panic for expected failures.
  - Never ignore an error silently: handle it, return it, or `_ =` it with a
    reason in a comment.
- **No package-level mutable state.** Pass dependencies through constructors.
  Time, randomness and identifiers come from ports (`core.Clock`, `core.Ids`)
  or, in flow code, from `DurableContext`.
- **Concurrency:**
  - Every goroutine has an owner that waits for it or cancels it.
  - Channels are closed by the sender.
  - Use `errgroup` for fan-out with errors.
- **Determinism in flow code:** no wall clock, no global randomness, no
  ranging over maps where order matters (sort the keys).
- **Logging:**
  - `log/slog` with structured fields.
  - No secrets, tokens or full prompts at info level.
  - Log or return an error, not both.
- **Naming:** clear over short; acronyms keep their case (`ID`, `URL`, `HTTP`);
  no stutter (`flow.Flow` → `flow.IR`).
- **Size:** prefer functions under ~60 lines and files under ~500 lines; split
  when a file holds two ideas.
- **Generated code** carries the standard `// Code generated … DO NOT EDIT.`
  header and is never edited by hand.

## 3. Tests

- Unit tests sit next to the code (`_test.go`), table-driven where cases vary
  by data. Use `t.Helper`, `t.TempDir` and `t.Context`.
- Behavior visible to users is a BDD scenario
  (`docs/engineering/bdd-convention.md`); unit tests cover the logic below it.
- No sleeps, no wall clock, no network. Use fakes from test-only packages and
  vendor simulators.
- A test asserts outcomes, not implementation details. It should fail for a
  real bug and survive a refactoring.

## 4. Dependencies

- Prefer the standard library, then libraries already in `go.mod`.
- A new dependency needs a reason in the commit body. It must:
  - be maintained;
  - have a license compatible with Apache-2.0: MIT, BSD, ISC or Apache-2.0
    (MPL-2.0 only when used unmodified). `make licenses` rejects forbidden
    and restricted licenses such as GPL and AGPL;
  - be pure Go (the binary is built with `CGO_ENABLED=0`).
- Pin exact versions. Heavy dependencies (servers, SDKs with large trees) live
  in their own module, as `runtime/temporal/embedded` does.
- A port vendor library (cloud SDKs, SQL drivers, NATS, gocloud, Temporal, Tink
  and the like) must not be imported from `internal/config`; add its import-path
  prefix to that layer's `deny_external` in the same change (ADR-0016). The
  module port packages (`platform`, `runtime`, and subpackages) stay out via the
  layer allow-list, not `deny_external`.

## 5. Documentation

- Docs describe the current design and behavior, in plain English, as if the
  project had always been this way. Decisions and their reasons live in ADRs.
- Change the doc in the same change as the behavior it describes.
- Prefer tables and lists over long paragraphs. Use Mermaid for diagrams, so
  they stay editable as text.
