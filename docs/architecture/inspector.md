# Run inspector

Status: **Implemented** (WP-L6-04, package `api/inspector`); the player uses
the rrweb recording of ADR-0009. Specs: `specs/inspector`.

The inspector is the web UI a person uses to see runs and to resolve their
gates. It shows; it does not decide. Authorization, tenant isolation and a
gate's payload rules stay in the API (`specs/auth`, `specs/flow-engine`), and
event order, live updates and reattach stay in `specs/streaming`.

## 1. Placement

The roadmap names `web/inspector` (TypeScript). This implementation serves the
same UI from `api/inspector` as server-rendered HTML with embedded CSS and
JavaScript, for three reasons:

- the product stays one Go binary (ADR-0001) and `make gate` runs only Go;
- a Go package under `web/` would need a new entry in `layering.yaml`, and
  changing the package graph needs an ADR;
- the inspector is an HTTP edge over the API, which is the layer it already
  belongs to.

The routes and the `Source` contract below are the stable part. A TypeScript
frontend can replace the templates without changing them.

## 2. Pages and routes

The handler is mounted with `http.StripPrefix(prefix, handler)` and
`Options.Prefix` set to the same value, so links and form actions are
absolute under it.

| Route | Shows |
| --- | --- |
| `GET /` | the runs the principal may see, with id, flow and state (REQ-INSP-001) |
| `GET /runs/{id}` | the run's state, error code, stored events, open gates and recording (REQ-INSP-004, -005, -008, -009, -011, -012) |
| `GET /runs/{id}/live?after=<seq>` | the same state and gates plus the events after `seq`, which the page polls while a run is open (REQ-INSP-003) |
| `POST /runs/{id}/gates/{gate}` | submits a payload; an accepted one returns to the run, a rejected one is explained with `invalid_argument` and the gate stays open (REQ-INSP-006, -007) |
| `GET /runs/{id}/recording` | the ordered rrweb frames for the player; `recording_missing` when the run has none (REQ-INSP-010, -011) |
| `GET /assets/*` | embedded CSS and JavaScript |

The page carries the data it renders as a `<script type="application/json">`
island. The embedded script hydrates it and keeps the run live; the journey
driver reads the same island, so the assertion is on what the page shows.

## 3. The tenant-scoped source

`inspector.Source` is the API boundary:

- `List`, `Run`, `Events` and `Resolve` each take the authenticated principal.
  The tenant comes from that principal, never from a request field.
- A run of another tenant is `not_found`, the same answer as a missing run, so
  the inspector cannot tell them apart (REQ-INSP-002).
- A missing read permission is `permission_denied` (REQ-INSP-013).
- `Resolve` validates the payload and returns `invalid_argument` with the
  API's reason; it never closes the gate itself on a rejection (REQ-INSP-007).

The handler does not trust the source with tenant isolation. `Run` sets
`Tenant` on each record; the handler renders a run only when it equals the
principal's tenant, and drops a listed run that does not. A source bug fails
closed.

## 4. Recording and playback

A run's `Recording` is either absent, present with ordered rrweb frames, or
marked as one that cannot be loaded. The detail page renders the three states
plainly: no recording to replay, cannot be replayed, or the frame count and a
replay that stops at the end. `GET /runs/{id}/recording` serves the frames the
rrweb-player consumes; the gate never loads a recording from a vendor.

## 5. Testing

- `api/inspector` unit tests cover tenant filtering, denial without data,
  sequence order, gate rendering and rejection, the three recording states,
  the live cursor, assets and the mount prefix.
- The `@journey` scenarios of `specs/inspector` are exercised by the driver in
  `internal/bdd/steps/inspector.go` until the browser runner (WP-L9-01)
  replays them; the driver uses a real handler over an in-memory source, no
  clock and no sleep.
