# L9 — Browser runner

Browser use and recording as a separate service (ADR-0009, `specs/browser`).

### WP-L9-01 — Browser runner contract and conformance suite
Depends: WP-L3-03, WP-L6-03, WP-L1-08.
Owns: `contracts/basamento/browser`, `browser/conformance`.
The contract, its conformance suite and fixture apps. The runner implementation lives in its own repository. Proves session-scoped worker tokens end to end (AUTH-005-*).
