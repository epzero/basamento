# Roadmap

Work is organized in three levels, so the roadmap stays readable as it grows:

| Level | Where | Example |
| --- | --- | --- |
| **Epic** | One file here, `L<n>-<name>.md` | `L3-models.md` |
| **Work package (WP)** | A `### WP-L<n>-<NN> — title` section in its epic file, with `Depends:` and `Owns:` lines | `WP-L3-04 — Providers and model routing` |
| **Task** | A piece of a WP small enough for one change (one vendor manifest, one adapter, one migration); a sub-issue where a tracker is used | "Seedance manifest and simulator" |

- These files hold definitions only. Status is derived, never written:
  `go tool gate wp` shows each WP as done (its scenarios are all active),
  in progress (a session holds it), ready or blocked. Agents coordinate as
  described in `docs/engineering/agent-workflow.md` §3.
- `@wp:WP-…` tags in `specs/` must name a WP defined here (`gate spec lint`).
- Besides the directories on its `Owns:` line, every WP owns its scenarios
  under `specs/<capability>/` and its step file in `internal/bdd/steps/`.
  Two WPs may own overlapping directories only when one depends on the other,
  so they never run in parallel (`gate docs` checks it).
- A WP may start when its dependencies are done. A WP carries a
  `@level:system` scenario only if it depends, directly or not, on `kit serve`
  (WP-L1-05), and on the API (WP-L6-03) when the scenario goes through it. WPs run in parallel when
  their owned directories do not overlap.
- When a WP grows too large for one change, split it into new WPs in the same
  epic (next free number), or into tasks when the pieces share one spec.

The diagrams below are generated from the epic files; refresh them with
`go tool gate docs -write`. The gate fails when they are out of date.

## Epics

<!-- BEGIN GENERATED: epics (go tool gate docs -write) -->
```mermaid
flowchart LR
  L0["L0 Foundation"]
  L1["L1 Platform ports"]
  L2["L2 Expressions and schemas"]
  L3["L3 Models"]
  L4["L4 Flow engine"]
  L5["L5 Runtimes"]
  L6["L6 Streaming and API"]
  L7["L7 Agents and tools"]
  L8["L8 Auth"]
  L9["L9 Browser runner"]
  L10["L10 Presets"]
  L0 --> L1
  L0 --> L2
  L1 --> L5
  L1 --> L8
  L2 --> L4
  L3 --> L6
  L4 --> L5
  L5 --> L3
  L6 --> L7
  L6 --> L9
  L7 --> L10
  L8 --> L3
```
<!-- END GENERATED: epics -->

## Work packages

<!-- BEGIN GENERATED: work-packages (go tool gate docs -write) -->
```mermaid
flowchart LR
  subgraph L0_epic["L0 Foundation"]
    WP_L0_01["WP-L0-01<br/>Repository conventions and gate"]
  end
  subgraph L1_epic["L1 Platform ports"]
    WP_L1_01["WP-L1-01<br/>SQL store and migrations"]
    WP_L1_02["WP-L1-02<br/>Blob store"]
    WP_L1_03["WP-L1-03<br/>Bus and KV"]
    WP_L1_04["WP-L1-04<br/>Secrets and payload encryption"]
    WP_L1_05["WP-L1-05<br/>Configuration, profiles and kit serve"]
    WP_L1_06["WP-L1-06<br/>Credential store"]
    WP_L1_07["WP-L1-07<br/>Telemetry"]
  end
  subgraph L2_epic["L2 Expressions and schemas"]
    WP_L2_01["WP-L2-01<br/>CEL environment"]
    WP_L2_02["WP-L2-02<br/>JSON Schema"]
  end
  subgraph L3_epic["L3 Models"]
    WP_L3_01["WP-L3-01<br/>Invocation contract and chat adapters"]
    WP_L3_02["WP-L3-02<br/>Structured output and middleware"]
    WP_L3_03["WP-L3-03<br/>Model node"]
    WP_L3_04["WP-L3-04<br/>Providers and model routing"]
    WP_L3_05["WP-L3-05<br/>Prompt templates"]
    WP_L3_06["WP-L3-06<br/>Model catalog and price table"]
    WP_L3_07["WP-L3-07<br/>Workers"]
    WP_L3_08["WP-L3-08<br/>Workers on demand"]
  end
  subgraph L4_epic["L4 Flow engine"]
    WP_L4_01["WP-L4-01<br/>IR and compiler"]
    WP_L4_02["WP-L4-02<br/>Flow machine"]
    WP_L4_03["WP-L4-03<br/>Go builder API"]
    WP_L4_04["WP-L4-04<br/>Flow host"]
  end
  subgraph L5_epic["L5 Runtimes"]
    WP_L5_01["WP-L5-01<br/>Local runtime"]
    WP_L5_02["WP-L5-02<br/>Temporal runtime"]
    WP_L5_03["WP-L5-03<br/>Embedded Temporal server"]
    WP_L5_04["WP-L5-04<br/>Runtime conformance suite"]
  end
  subgraph L6_epic["L6 Streaming and API"]
    WP_L6_01["WP-L6-01<br/>Durable event log"]
    WP_L6_02["WP-L6-02<br/>Live streaming and reattach"]
    WP_L6_03["WP-L6-03<br/>API surface"]
    WP_L6_04["WP-L6-04<br/>Run inspector UI"]
  end
  subgraph L7_epic["L7 Agents and tools"]
    WP_L7_01["WP-L7-01<br/>Agent loop"]
    WP_L7_02["WP-L7-02<br/>Tools and MCP"]
    WP_L7_03["WP-L7-03<br/>Conversations"]
  end
  subgraph L8_epic["L8 Auth"]
    WP_L8_01["WP-L8-01<br/>Authentication modes"]
    WP_L8_02["WP-L8-02<br/>Authorization"]
  end
  subgraph L9_epic["L9 Browser runner"]
    WP_L9_01["WP-L9-01<br/>Browser runner contract and conformance suite"]
  end
  subgraph L10_epic["L10 Presets"]
    WP_L10_01["WP-L10-01<br/>Codegen preset"]
  end
  WP_L0_01 --> WP_L1_01
  WP_L0_01 --> WP_L1_02
  WP_L0_01 --> WP_L1_03
  WP_L0_01 --> WP_L1_04
  WP_L0_01 --> WP_L2_01
  WP_L0_01 --> WP_L2_02
  WP_L0_01 --> WP_L3_05
  WP_L1_01 --> WP_L1_05
  WP_L1_01 --> WP_L1_06
  WP_L1_01 --> WP_L1_07
  WP_L1_01 --> WP_L6_01
  WP_L1_02 --> WP_L1_05
  WP_L1_02 --> WP_L3_03
  WP_L1_02 --> WP_L5_02
  WP_L1_03 --> WP_L1_05
  WP_L1_03 --> WP_L6_02
  WP_L1_04 --> WP_L1_05
  WP_L1_04 --> WP_L1_06
  WP_L1_04 --> WP_L5_02
  WP_L1_05 --> WP_L10_01
  WP_L1_05 --> WP_L5_03
  WP_L1_05 --> WP_L8_01
  WP_L1_06 --> WP_L3_01
  WP_L2_01 --> WP_L4_01
  WP_L2_02 --> WP_L3_01
  WP_L2_02 --> WP_L4_01
  WP_L3_01 --> WP_L3_02
  WP_L3_01 --> WP_L3_06
  WP_L3_02 --> WP_L7_01
  WP_L3_03 --> WP_L3_04
  WP_L3_03 --> WP_L6_02
  WP_L3_04 --> WP_L3_07
  WP_L3_06 --> WP_L3_03
  WP_L3_07 --> WP_L3_08
  WP_L4_01 --> WP_L4_02
  WP_L4_01 --> WP_L4_03
  WP_L4_02 --> WP_L4_04
  WP_L4_04 --> WP_L5_01
  WP_L4_04 --> WP_L5_02
  WP_L5_01 --> WP_L3_03
  WP_L5_01 --> WP_L5_04
  WP_L5_01 --> WP_L6_01
  WP_L5_01 --> WP_L7_01
  WP_L5_02 --> WP_L5_03
  WP_L5_03 --> WP_L5_04
  WP_L6_01 --> WP_L6_02
  WP_L6_02 --> WP_L6_03
  WP_L6_02 --> WP_L7_03
  WP_L6_03 --> WP_L6_04
  WP_L6_03 --> WP_L9_01
  WP_L7_01 --> WP_L7_02
  WP_L7_01 --> WP_L7_03
  WP_L7_02 --> WP_L10_01
  WP_L8_01 --> WP_L3_07
  WP_L8_01 --> WP_L8_02
  WP_L8_02 --> WP_L6_03
```
<!-- END GENERATED: work-packages -->
