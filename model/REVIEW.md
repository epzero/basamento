# Review guide: models

High risk where money moves (`model/node`, `model/catalog`): a bug charges twice or never.

- Every attempt of one logical start carries the same client ref.
- An unknown start outcome leads to `Lookup` or a gate, never a blind restart.
- A paid start is preceded by a reservation and followed by settlement on
  every path: success, failure, cancellation, crash recovery.
- Prices come from `model/catalog`'s price table (the shipped snapshot,
  deployment overrides and tenant overrides, most specific layer wins) and from
  the model's CEL price formula when the chosen entry has no formula; no
  hard-coded vendor prices.
- Reject negative rates at set time or snapshot load; reject negative usage
  counts and negative priced amounts at pricing time (REQ-MODEL-084,
  REQ-MODEL-085, REQ-MODEL-086).
- A model's own CEL price formula must return a whole number of milli-points;
  a fractional or non-integer float fails pricing, never rounded to a credit
  (REQ-MODEL-087). The default token formula may round half up
  (REQ-MODEL-076).
- Fail closed on pricing: an unknown model id, no price in any layer, no
  holding rate rule, or an invalid price entry is an error; never a default
  price (REQ-MODEL-068, REQ-MODEL-073).
- Any map or slice in `model/` that grows with invocations must have a
  bounded retention policy with tests (REQ-MODEL-088): an ended invocation is
  kept only until end plus its window and while under the ended count; evict
  oldest ended first with a deterministic tie-break at equal end times; never
  evict a running invocation; sweep inline on registry methods, not a timer or
  goroutine per invocation.
- Provider adapters ship with a simulator and contract tests; the gate never
  calls a live vendor.
- Credentials are resolved at the edge of a step and never logged or stored in
  results.