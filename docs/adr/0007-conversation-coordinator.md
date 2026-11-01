# ADR-0007 — Conversations: a coordinator run with one child run per turn

**Status:** accepted · **Date:** 2026-11-01

## Context
With one independent run per turn, two turns can overlap and interleave writes to the conversation unless a per-conversation lock exists, with recovery and a custom queue. With one long run per conversation, ordering and steering come free, but long-lived workflows need versioning and bounded history.

## Decision
A thin coordinator run per conversation receives messages as gate resolutions and starts one child run per turn. Messages arriving during a turn follow a policy: `queue`, `steer` (delivered at the next step boundary) or `cancel` (partial message kept, marked interrupted). The coordinator checkpoints regularly; turn logic lives in short-lived children, which limits versioning to a small surface.
