# Conversations: a coordinator run with one child run per turn

Status: **Planned** (WP-L7-03). Design: ADR-0007, `docs/architecture/streaming.md` §6.
The agent loop of one turn is specified in the `agent` capability (WP-L7-01);
the messages a conversation exchanges and their text are `Message` values
(`specs/glossary.md`). Streaming, detach and reattach are specified in
`specs/streaming` and are not repeated here.

### REQ-CONV-001 — One coordinator run per conversation
While a conversation is open, the conversation coordinator shall be a single run, named `conv-<id>`, that starts every child run of the conversation.

### REQ-CONV-002 — One child run per turn
When a user message starts a turn, the conversation coordinator shall start exactly one child run for it, and run the next turn only after that child run has reached `completed`, `failed` or `cancelled`.

### REQ-CONV-003 — Messages sent during a turn are queued
While a turn is running, when a user message arrives with mode `queue` (the default), the conversation coordinator shall queue it for the next turn.

### REQ-CONV-004 — Queued turns run in the order sent
When a queued message's turn starts, the conversation coordinator shall start the turn of the queued message that arrived first among those still queued.

### REQ-CONV-005 — A message delivered twice arrives once
When the conversation coordinator receives a user message with a message id it has already accepted, the conversation coordinator shall run no second turn for it.

### REQ-CONV-006 — A message marked steer redirects the running turn
While a turn is running, when a user message arrives marked `steer`, the conversation coordinator shall deliver it to that turn's child run at its next step boundary.

### REQ-CONV-007 — Steering keeps the partial output and continues the turn
When a child run receives a steering message, the conversation coordinator shall keep the message it was writing, append the steering message to the history the turn sees, and continue the turn in the same child run.

### REQ-CONV-008 — A message marked cancel cancels the running turn
While a turn is running, when a user message arrives marked `cancel`, the conversation coordinator shall cancel that turn's child run.

### REQ-CONV-009 — A cancelled turn is reported as interrupted
When a child run is cancelled, the conversation coordinator shall end the turn with state `cancelled` and code `cancelled` and record the model message the turn was writing as interrupted with the text received so far.

### REQ-CONV-010 — Cancelling a turn keeps the queued messages
When a running turn is cancelled, the conversation coordinator shall keep every message queued during it and run their turns in arrival order until the queue is empty.

### REQ-CONV-011 — Cancelling a conversation stops turns and queued messages
When a conversation is cancelled, the conversation coordinator shall cancel the running turn, discard the queued messages, and end the conversation's coordinator run in state `cancelled`.

### REQ-CONV-012 — A new turn sees prior turns and the message that starts it
When a turn starts, the conversation coordinator shall give its child run the user and model messages of the conversation's completed, failed and cancelled turns (with partial model messages marked interrupted), in order, followed by the user message that starts the turn.

### REQ-CONV-013 — A failed turn leaves the conversation usable
When a turn's child run fails, the conversation coordinator shall record what it produced as a failed turn and keep the conversation open for a further user message.

### REQ-CONV-014 — The coordinator continues as new
Where the runtime supports continuing as new, when the conversation coordinator reaches a turn boundary after it has made at least its checkpoint threshold of durable calls (1000 by default) since it last started, the conversation coordinator shall checkpoint and continue as a new run of the same conversation.

### REQ-CONV-016 — Conversations belong to one tenant and one user
While a conversation is open, the conversation coordinator shall run every turn of it, and read and write its history, only for the conversation's tenant and user.

### REQ-CONV-017 — Another tenant's conversation is not found
When a principal asks the conversation coordinator for a conversation of another tenant, the conversation coordinator shall fail with code `not_found`.

### REQ-CONV-018 — Another user's conversation is denied
When a user reads, sends a message to, or cancels a conversation of another user in the same tenant, the conversation coordinator shall fail with code `permission_denied`.

### REQ-CONV-019 — A cancelled conversation accepts no message
When a user sends a message to a cancelled conversation, the conversation coordinator shall refuse it with code `conflict`.

### REQ-CONV-020 — Steering with no turn running is refused
While no turn is running, if a user message arrives marked `steer`, then the conversation coordinator shall refuse it with code `conflict`.

### REQ-CONV-021 — Cancelling a turn with no turn running is refused
While no turn is running, if a user message arrives marked `cancel`, then the conversation coordinator shall refuse it with code `conflict`.
