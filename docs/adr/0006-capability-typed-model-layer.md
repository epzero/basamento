# ADR-0006 — Capability-typed model layer with one invocation contract

**Status:** accepted · **Date:** 2026-11-01

## Context
Products call chat, embedding, image, video and audio models. Vendors answer
in different ways: one streamed reply, one reply, submit-and-poll, webhooks,
WebSockets, local command lines. Inside a run, a chat reply is as asynchronous
as a video: it outlives the client connection (ADR-0005), streams, can be
cancelled, is retried and must be recorded.

## Decision
- Models are described by capability, parameter JSON Schema, input slots, a
  CEL price formula, vendor protocol and locality.
- **Every model call is an invocation** through one contract, `model.Invoker`:
  `Start` (idempotent per client ref), `Events` (ordered partial output),
  `Result`, `Cancel`, `Lookup`. `model.Invoke` is the blocking form. Vendor
  protocols are adapter details.
- Providers are mostly generic and configured by manifests: an HTTP API
  provider and a command-line provider. Chat and embedding adapters wrap
  `eino-ext` and official SDKs. Remote services implement
  `basamento.invoke.v1.InvocationService`.
- In flows, one `model` node kind handles every capability:
  1. validate;
  2. reserve;
  3. start with a stable client ref;
  4. follow events;
  5. wait;
  6. ingest outputs;
  7. settle.

  An ambiguous start is resolved by lookup or a human gate, never by starting again blindly.

- Prices are data. A **price table** holds, per model, ordered rate rules;
  a rule's `when` (CEL over the start time, `params`, `usage` and tenant)
  covers off-peak hours, tiers and dated changes. The table ships with the
  binary and is overridden per deployment and per tenant. A model's price
  formula is CEL over `params`, `usage` and the chosen `rates`: evaluated on
  estimated usage to reserve, and on actual usage to settle when the vendor
  reports no cost of its own.

## Consequences
- Budget, cancellation, reattach and replay work the same for every model.
- Embeddings and other small calls pay no durable overhead when called in process.
- Cost safety is specified as P0 scenarios in `specs/models`.
