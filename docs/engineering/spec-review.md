# Spec review: agents approve, the owner reviews by priority

Specs are written by the designer's spec author and approved by the
reviewer's **spec reviewer**, a session that did not write them, on the spec
pull request (`agent-workflow.md` §7.3). The owner reviews later, in order
of importance. Tooling: `go tool gate spec …`.

## 1. Flow

```
spec author ──► specs committed ──► spec reviewer ──► `gate spec approve`
                                         │                  │
     changes requested (≤ 3 rounds, then the architect)       ▼
                               specs/<cap>/spec.lock.yaml  (hash, tier, author, reviewer, note, human: pending)
                                                            ▼
                                     the owner works the queue: `gate spec queue`, P0 first
```

Review discussion is transient: it happens on the pull request and in the
tracker.
The repository keeps the decision, not the conversation.

## 2. Reviewer rules

- A different session from the author (`docs/engineering/agent-workflow.md`
  §1), preferably on a different model.
- Works on the spec pull request's branch (`gate agent new … -branch`,
  `agent-workflow.md` §1), so the ledger commit lands on the text it approves.
- Reads only. It never edits the spec under review; it requests changes, with
  exact replacement text for blocking ones.
- Checks every scenario against the rubric (§3).
- Approves with its own identity as reviewer:

  ```bash
  go tool gate spec approve -ids MODEL-004-01,MODEL-005-01 -tier P0 \
    -author claude-code:spec-author:34bca5 \
    -note "Money safety: idempotent starts and ambiguous-start handling." \
    -ref <link to the review discussion, optional>
  ```

  `-unapproved` selects every scenario whose text is not yet approved.

## 3. Rubric

| # | Check |
| --- | --- |
| R1 | EARS pattern is right; one behavior per requirement; testable; unambiguous. |
| R2 | Scenario is declarative, one behavior, glossary terms, ≤ 12 steps, concrete Then-steps. |
| R3 | Scenario demonstrates its requirements; important negative and edge cases exist. |
| R4 | Consistent with the constitution, ADRs and other specs; no duplicate behavior. |
| R5 | Deterministic in the gate, or in a labelled lane (`@live`, `@eval`, `@journey`). |
| R6 | Tier assigned, with a note for P0 and P1. |

## 4. Tiers

The tier is the **more critical** of the reviewer's judgment and the rule
minimum computed by the tool.

| Tier | Meaning | Rule minimum when | Human review | Effect |
| --- | --- | --- | --- | --- |
| **P0** | Critical | tags `@security`, `@cost`, `@contract`; the reviewer judges data loss, durability, determinism or a public contract | within 1 business day | **Release blocked** (`gate release-check`) until a person accepts |
| **P1** | Changes agreed behavior | the scenario's approved text changed | within a week | Listed in release notes |
| **P2** | New behavior | (reviewer judgment) | batched | none |
| **P3** | Editorial | (reviewer judgment) | spot-check | none |

## 5. The ledger: `specs/<capability>/spec.lock.yaml`

One file per capability, so parallel approvals do not collide.

```yaml
scenarios:
  MODEL-004-01:
    hash: sha256:…                 # approved text: name, steps, tables, Background, expanded Examples (tags excluded)
    reqs: [REQ-MODEL-004]          # requirement tags it was approved with
    tier: P0
    author: claude-code:spec-author:34bca5
    reviewer: antigravity:spec-reviewer:7c1e0d
    approved_at: "2026-11-01"
    note: 'Money safety: a retry must never cause a second charge.'   # required for P0 and P1
    ref: https://…                 # optional link to the discussion
    human:
      status: pending              # pending | accepted | rejected
      # once decided: by: human:epzero, at: <date>, note: <link to the answer in the tracker>
```

`gate spec lock` fails when:
- a scenario has no entry, or its text, requirement tags or capability changed;
- a tier is below the rule minimum, or a P0 or P1 entry has no note;
- an identity is malformed, the author is not a `spec-author`, the reviewer is
  not a `spec-reviewer`, or both are the same instance;
- a person rejected the scenario, or an acceptance or rejection names no
  person.

## 6. Human review

`go tool gate spec queue` lists pending scenarios, P0 first, with their notes.
The owner answers on the acceptance ticket the reviewer opened
(`agent-workflow.md` §5) and the designer's lead records the outcome in a
pull request. When no personal login is given, `-by` defaults to `epzero`
(`human:epzero`); the note links the answer, which is the proof:

```bash
go tool gate spec human -id STRM-003-01 -status accepted -note "<link to the answer>"
go tool gate spec human -id MODEL-005-02 -status rejected -note "lookup must time out: <link>"
```

A **rejected** scenario fails the gate until a spec change resolves it.
Approving the same text again does not: a person's decision stays with the
text it was made about, and the tier of unchanged text never becomes less
critical.

## 7. Changing an approved scenario

Edit the text and review again. The tool raises the tier to at least P1,
because agreed behavior changes. Deleting a scenario removes its ledger entry
in the same change, with the reason in the commit message.
