# Inspector: the run inspector web UI

Status: **Implemented** (WP-L6-04). The run inspector is a web UI
(`api/inspector`, server-rendered with embedded assets) that reads runs through
the API of WP-L6-03: it lists the runs a principal may see, shows one run's
durable events as they arrive, resolves the run's open gates, and replays the
rrweb recording of a browser session attached to the run. Design: ADR-0005,
ADR-0009, `docs/architecture/streaming.md` §2, §3,
`docs/architecture/browser-runner.md` §3.

The inspector shows; it does not decide. Authorization, tenant isolation and
the payload rules of a gate stay in the API (`specs/auth`,
`specs/flow-engine`), and the event order, live updates and reattach stay in
`specs/streaming`. The inspector only runs live while its run is live: what a
person sees after leaving, or after a run ends, is the stored history
(REQ-INSP-003).

### REQ-INSP-001 — The runs a person may see
The inspector shall list the runs of the principal's projects that the principal has permission to read, with each run's id, flow and state.

### REQ-INSP-002 — No other tenant's run
The inspector shall show no record of a tenant other than the principal's tenant.

### REQ-INSP-003 — Live while the run is live
While the run a person has open is running, the inspector shall add each new durable event to that run's events as the API delivers it.

### REQ-INSP-004 — History after a reload
When a person opens a run, the inspector shall show its stored durable events in sequence order.

### REQ-INSP-005 — Open gates
When a run waits at one or more gates, the inspector shall show each open gate with its node id and the payload it accepts.

### REQ-INSP-006 — Resolving a gate
When a person resolves an open gate with a payload, the inspector shall submit that payload to the gate.

### REQ-INSP-007 — A rejected payload is explained
If the gate rejects a payload, then the inspector shall explain the rejection with the API's reason and keep the gate listed as open.

### REQ-INSP-008 — Failed runs
Where a run has state `failed`, the inspector shall show the state `failed` and the run's error code.

### REQ-INSP-009 — Cancelled runs
Where a run has state `cancelled`, the inspector shall show the state `cancelled` and no error code.

### REQ-INSP-010 — Replaying a browser session
Where a run has an rrweb recording of a browser session, the inspector shall let a person replay that recording.

### REQ-INSP-011 — A run with no recording
If a run has no rrweb recording, then the inspector shall say that there is no recording to replay.

### REQ-INSP-012 — A recording that cannot be loaded
If an rrweb recording fails to load, then the inspector shall say the recording cannot be replayed.

### REQ-INSP-013 — Without permission
If the principal lacks the permission to read a run, then the inspector shall deny access to that run and show nothing of it.
