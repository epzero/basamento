# Bus and KV: messages with replay, entries with a TTL

Status: **Planned** (WP-L1-03). Design: ADR-0004, ADR-0005, [`design.md`](design.md).

The `Bus` and `KV` ports of `platform`, their embedded implementation
(`nats-server` in-process with JetStream), their cloud implementation (a NATS
cluster) and their in-process fake for unit tests. The streaming layer
(`specs/streaming`) builds deltas, relays and snapshots on top of them.

## Implementations

### REQ-BUS-001 — One behavior on every implementation
The in-process fake, the embedded implementation and the cloud implementation of the bus and the KV store shall each satisfy every requirement of this capability that is not scoped to a single profile.

## Bus

### REQ-BUS-002 — Increasing bus sequences
When a message is published on a subject, the bus shall store it and return a bus sequence greater than the bus sequence of every earlier message on that subject.

### REQ-BUS-003 — Replay after a bus sequence, then live
When a subscriber subscribes to a subject from the start or after a bus sequence, the bus shall deliver every retained message of that subject with a greater bus sequence, followed by the messages published later, in bus-sequence order and each exactly once.

### REQ-BUS-004 — Live subscription
When a subscriber subscribes live to a subject, the bus shall deliver only the messages of that subject published after the subscription started, in bus-sequence order and each exactly once.

### REQ-BUS-005 — Exact subjects
The bus shall deliver to a subscription only the messages published on exactly the subject it subscribed to.

### REQ-BUS-006 — Same order for every subscriber
While several subscribers are subscribed to one subject, the bus shall deliver its messages to each of them in the same order.

### REQ-BUS-007 — Purge
When a subject is purged, the bus shall drop the retained messages of that subject only, and shall keep assigning later messages on it greater bus sequences than any it assigned before.

### REQ-BUS-008 — Replay across a purge is refused
If a subscriber subscribes to a subject after a bus sequence lower than the last bus sequence purged from that subject, then the bus shall refuse the subscription with code `not_found`, reporting that last purged bus sequence.

### REQ-BUS-009 — Ending a subscription
When a subscriber cancels its subscription, the bus shall end that subscription and deliver no further message to it.

### REQ-BUS-010 — A slow subscriber does not hold anyone up
If a subscriber has as many undelivered messages as its subscription buffer holds and another message arrives for it, then the bus shall end that subscription after the buffered messages, without delaying publishers or other subscribers.

### REQ-BUS-011 — Message size limit
If a published message is larger than the maximum message size, then the bus shall refuse the publish with code `invalid_argument` and store nothing.

### REQ-BUS-012 — Invalid arguments
If a caller passes an empty or malformed subject or key, or a negative TTL, then the bus or KV store shall refuse the call with code `invalid_argument` and store nothing.

### REQ-BUS-013 — Unreachable server
If the bus server cannot be reached, then the bus and the KV store shall fail each call with code `unavailable` instead of waiting for the server to return.

### REQ-BUS-014 — Embedded durability
Where the profile is `embedded`, when the bus is stopped and started again on the same data directory, the bus shall keep every message not purged, the last purged bus sequence and the bus sequences of each subject, and every KV entry and its revision.

## KV store

### REQ-BUS-015 — Put and read
When a KV entry is put, the KV store shall keep its value with a revision greater than every earlier revision of that key, and a read of the key shall return the latest value and its revision.

### REQ-BUS-016 — Compare-and-swap
When a KV entry is swapped at a revision, the KV store shall replace its value and return a greater revision if that revision is the entry's current one, and otherwise shall fail with code `conflict` and leave the entry unchanged.

### REQ-BUS-017 — Create
When a KV entry is created, the KV store shall store it if the key has no entry, and otherwise shall fail with code `conflict` and leave the existing entry unchanged.

### REQ-BUS-018 — Expiry
While the clock is at or past the time a KV entry with a TTL was put, swapped or created plus its TTL, the KV store shall treat the entry as absent.

### REQ-BUS-019 — Creating an expired key
While a KV entry has expired, when a caller creates the key, the KV store shall store the new entry and return a new revision.

### REQ-BUS-020 — Renewal
When a KV entry is swapped with a TTL before it expires, the KV store shall count its TTL from the time of the swap.

### REQ-BUS-021 — Delete
When a KV entry is deleted, the KV store shall treat the key as having no entry.

### REQ-BUS-022 — Reading a key without an entry
If a caller reads a key that has no entry, then the KV store shall fail the read with code `not_found`.

### REQ-BUS-023 — Entries without a TTL
When a KV entry is put, swapped or created without a TTL, the KV store shall keep the entry regardless of how much time passes.

### REQ-BUS-024 — Deleting a key without an entry
When a caller deletes a key that has no entry, the KV store shall succeed and leave the key without an entry.

### REQ-BUS-025 — Swapping an expired entry
If a caller swaps a KV entry at the revision of an entry that has expired, then the KV store shall fail the swap with code `conflict`.
