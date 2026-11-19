# Review guide: models

High risk where money moves (`model/node`): a bug charges twice or never.

- Every attempt of one logical start carries the same client ref.
- An unknown start outcome leads to `Lookup` or a gate, never a blind restart.
- A paid start is preceded by a reservation and followed by settlement on
  every path: success, failure, cancellation, crash recovery.
- Prices come from `model/catalog`'s layers and the model's CEL formula; no
  hard-coded vendor prices.
- Provider adapters ship with a simulator and contract tests; the gate never
  calls a live vendor.
- Credentials are resolved at the edge of a step and never logged or stored in
  results.
