# Prompt: versioned templates, segments and untrusted input

Status: **Implemented** (WP-L3-05). Package: `prompt` (imports only `core`).
Design: [`design.md`](design.md), `docs/architecture/overview.md` §3.

A **prompt template** is stored under an id and a version and never changes
once registered. It declares its inputs and is made of **segments**, each of
a kind (`static`, `semi-static`, `dynamic`, `history`, `user`) and with an
optional **segment budget**. The **assembler** turns a template reference and
inputs into an **assembled prompt**: segments in a fixed order, history
trimmed to fit, a **cache hint** after the **cacheable prefix**, and every
**untrusted value** wrapped so it cannot pass for instructions. **Prompt
lint** checks templates before they are registered and assembled prompts
before they are sent.

Sizes and budgets are counted in characters (Unicode code points) of the
content, as `design.md` §3 defines.

## Versioned templates

### REQ-PROMPT-001 — Versioned identity
The prompt registry shall identify every template by id and version and resolve a reference `<id>@<version>` to the content registered under exactly that id and version.

### REQ-PROMPT-002 — Versions are immutable
If a template is registered under an id and version that already holds different content, then the prompt registry shall reject it with code `conflict` and keep the content registered first.

### REQ-PROMPT-003 — References are pinned
If a template reference names no version, then the prompt registry shall reject it with code `invalid_argument`.

### REQ-PROMPT-022 — References must resolve
If a template reference names an id and version that is not registered, then the prompt registry shall reject it with code `not_found`.

### REQ-PROMPT-004 — Variables are declared
If a template uses a variable it does not declare as an input, then the prompt registry shall reject the template with code `invalid_argument` naming the variable.

### REQ-PROMPT-005 — Inputs are strict
If an assembly is given no value for a declared input, or a value for an input the template does not declare, then the assembler shall fail with code `invalid_argument` naming the input.

### REQ-PROMPT-006 — Values are inserted literally
The assembler shall insert every input value and history entry literally, without expanding template syntax the value contains.

### REQ-PROMPT-007 — Deterministic assembly
The assembler shall produce the same assembled prompt, with the same content hash, for the same template version, inputs and history, whatever the order in which the inputs are supplied.

## Segments, budgets and cache hints

### REQ-PROMPT-008 — Segment order
The assembler shall place segments in the order `static`, `semi-static`, `dynamic`, `history`, `user`, and segments of the same kind in the order the template declares them.

### REQ-PROMPT-009 — Segment budgets
If a segment other than `history` is larger than its budget, then the assembler shall fail with code `invalid_argument` naming the segment, its size and its budget.

### REQ-PROMPT-010 — History is trimmed from the oldest entry
When the history segment is larger than its budget, or the assembled prompt is larger than the template's total budget, the assembler shall drop whole history entries, oldest first, until both fit, and shall report the number of entries it dropped.

### REQ-PROMPT-011 — Total budget
If the assembled prompt is larger than the template's total budget with no history entry left to drop, then the assembler shall fail with code `invalid_argument` naming the total budget and the size.

### REQ-PROMPT-012 — Cache hint
Where an assembled prompt has a `static` or `semi-static` segment, the assembler shall mark one cache hint at the end of the last such segment, and none otherwise.

### REQ-PROMPT-013 — Stable cacheable prefix
The assembler shall produce the same cacheable prefix, with the same prefix hash, for assemblies of one template version with the same `semi-static` inputs, whatever their `dynamic` inputs, `user` inputs and history.

## Untrusted input

### REQ-PROMPT-014 — Untrusted values are wrapped
The assembler shall wrap every value of an input not declared `trusted`, and every history entry, in untrusted-content delimiters labelled with the input's name, or with `history` for a history entry.

### REQ-PROMPT-015 — Delimiter break-out is neutralized
If an untrusted value contains text that reads as an untrusted-content delimiter, in any letter case or spacing, then the assembler shall neutralize it, so that the wrapped value contains exactly one opening and one closing delimiter: its own.

## Prompt lint

### REQ-PROMPT-016 — No variables in static segments
If a `static` segment uses a variable, then prompt lint shall report the error `variable_in_static` for that segment.

### REQ-PROMPT-017 — No untrusted input in the cacheable prefix
If a `static` or `semi-static` segment uses an input not declared `trusted`, then prompt lint shall report the error `untrusted_in_cacheable` for that segment.

### REQ-PROMPT-018 — Fixed text within budget
If the fixed text of a segment, without its variables, is larger than the segment's budget, then prompt lint shall report the error `fixed_text_over_budget` for that segment.

### REQ-PROMPT-019 — Unbounded segments are flagged
If a `dynamic`, `history` or `user` segment has no budget, then prompt lint shall report the warning `no_budget` for that segment.

### REQ-PROMPT-020 — Lint errors block registration
If prompt lint reports an error for a template, then the prompt registry shall reject the template with code `invalid_argument` listing the errors.

### REQ-PROMPT-023 — Lint warnings do not block registration
When prompt lint reports only warnings for a template, the prompt registry shall register the template and return the warnings.

### REQ-PROMPT-021 — Assembled prompts carry only their own delimiters
When an assembled prompt is linted, prompt lint shall report the error `unpaired_delimiter` for every untrusted-content delimiter that is not one of the pairs the assembler wrapped a value in.
