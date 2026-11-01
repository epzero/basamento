# Review guide

What every code review in this repository checks. Areas with their own risks
add a `REVIEW.md` next to their code; reviewers read every `REVIEW.md` on the
path from the root to each changed file, taken from the base branch
(`go tool gate review plan` lists them). How reviews run:
`docs/engineering/code-review.md`.

## Always

- **Specs:** the change does what its active scenarios say, and nothing in it
  contradicts an approved scenario, an ADR or the constitution.
- **Correctness:** error paths, boundaries, nil and empty inputs, concurrency
  (every goroutine has an owner; contexts are passed and honored).
- **Determinism:** flow code and tests use no wall clock, global randomness or
  map order.
- **Layering:** imports follow `layering.yaml`; test doubles stay in test-only packages.
- **Security:** no secrets in logs, errors, state or history; untrusted input
  is validated at the edge.
- **Contracts:** protobuf, event and error contracts only grow.
- **Guidelines:** `docs/engineering/coding-guidelines.md`, especially comments
  that explain why and docs that move with the code.
- **Size:** the change is small enough to review in one sitting; if not, ask
  for it to be split.

## How to report

- Failure here looks like: a scenario that passes while the behavior is
  wrong, a run that is lost or replayed differently, a doc that lies.
- Cite a file and line as evidence, never a guess from a name.
- Start with the number of findings; say plainly when there is nothing to fix.
- At most three nits. A problem the change did not introduce is
  `pre-existing`, never the author's to fix here.

## Skip

- Formatting and lint findings (the gate covers them), personal style
  preferences, and speculative refactors outside the change.
- Generated content: `go.sum`, the mermaid blocks `gate docs` writes, and
  `spec.lock.yaml` hashes (the gate checks them).
