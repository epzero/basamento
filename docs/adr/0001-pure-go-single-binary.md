# ADR-0001 — Pure Go, one static binary, two profiles

**Status:** accepted · **Date:** 2026-11-01

## Context
The platform ships as an OSS edition for self-hosting and single-user daily use, and as a SaaS. Coding agents build it. The Go ecosystem covers durable execution (Temporal is written in Go), CEL (`cel-go`), NATS, SQLite (`modernc.org/sqlite`) and object storage (`gocloud.dev`) in pure Go.

## Decision
- The backend is Go, built with `CGO_ENABLED=0` into one binary (`kit`).
- Two profiles: `embedded` (everything in-process; performance only needs to suit one user) and `cloud` (managed services). The same code and ports serve both (ADR-0004).
- UIs (run inspector, product frontends) are TypeScript and use generated connect clients.
- Capabilities that are weak in Go run as separate services behind a contract: browser use and recording (ADR-0009), local ML inference.

## Consequences
- Simple distribution and one language for the core. A gate check enforces `CGO_ENABLED=0`.
- Go's gaps cost something: browser automation with Playwright is a separate service; eval frameworks are replaced by `@eval` flows; non-OpenAI tokenizers are approximate.
