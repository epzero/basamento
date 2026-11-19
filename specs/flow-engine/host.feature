@wp:WP-L4-04 @level:component
Feature: The flow host
  The flow host is the one durable entrypoint (ADR-0011). It runs the flow
  definition a run was started with, drives the pure flow machine through the
  durable context of the run, turns each command of the machine into the
  matching durable call, and dispatches the effectful nodes whose kind has a
  registered node handler to that handler. These scenarios run the flow host
  against a recording fake of the durable context, so a run's durable calls
  can be read back exactly.

  Background:
    Given a recording fake durable context

  @REQ-FLOW-027 @id:FLOW-027-01
  Scenario: A run keeps its pinned definition when another version is registered
    Given flow "review" version 1 whose node "plan" of kind "agent" with output "plan" has a registered node handler and precedes node "done" of kind "set" setting "v" to 1
    And flow "review" version 1 is registered
    And a run of "review" version 1 starts and waits at the node "plan"
    And flow "review" version 2, where node "plan" with output "plan" precedes node "done" of kind "set" setting "v" to 2, is registered
    When the node handler of "plan" returns {"ok": true}
    Then the run completes with state {"plan": {"ok": true}, "v": 1}

  @REQ-FLOW-028 @id:FLOW-028-01
  Scenario Outline: A node kind without a registered handler fails the run before any node runs
    Given flow "triage" whose node "prep" of kind "tool" with node spec {"tool": "t1"} precedes the node "worker" of kind "<kind>"
    And no node handler is registered for the kind "<kind>"
    When a run of "triage" starts with input {}
    Then the run fails with code "unsupported" naming node "worker"
    And the run made no step, timer, gate wait, child run or durable event

    Examples:
      | kind  |
      | model |
      | agent |

  @REQ-FLOW-029 @id:FLOW-029-01
  Scenario Outline: A tool or task node runs as one step
    Given a flow whose node "worker" of kind "<kind>" maps in the input from "state.draft", writes output to "worker", and has the node spec {"<kind>": "w1"}
    And the step "w1" returns {"said": "done"}
    When a run of the flow starts with input {"draft": {"text": "hello"}, "extra": 1}
    Then the run made exactly one step, named "w1", with input {"text": "hello"}
    And the run completes with state worker = {"said": "done"}

    Examples:
      | kind |
      | tool |
      | task |

  @REQ-FLOW-030 @id:FLOW-030-01
  Scenario Outline: A node of a kind with a registered handler runs through that handler
    Given a flow whose node "worker" of kind "<kind>" has a registered node handler
    And the node "worker" maps in the input from "state.draft", writes output to "worker", and has the node spec {"<kind>": "w1"}
    And the node handler of "worker" returns {"said": "done"}
    When a run of the flow starts with input {"draft": {"text": "hello"}, "extra": 1}
    Then the node handler of "worker" was called with node "worker" and input {"text": "hello"}
    And the run completes with state worker = {"said": "done"}

    Examples:
      | kind  |
      | model |
      | agent |

  @REQ-FLOW-031 @id:FLOW-031-01
  Scenario: A wait node waits for its duration
    Given a flow whose node "cool_down" of kind "wait" has the node spec {"duration": "30s"}
    And a run of the flow has started
    When 30 seconds pass
    Then the run made a timer for 30s
    And the run completed node "cool_down" with no result

  @REQ-FLOW-031 @id:FLOW-031-02
  Scenario: A wait node is not complete before its duration passes
    Given a flow whose node "cool_down" of kind "wait" has the node spec {"duration": "30s"}
    And a run of the flow has started
    When 29 seconds pass
    Then the run made a timer for 30s
    And the run is waiting

  @REQ-FLOW-032 @id:FLOW-032-01
  Scenario: A gate node waits for the gate to be resolved
    Given a flow whose node "approve" of kind "gate" has the node spec {"schema": "review.json", "accept_if": "input.approved", "timeout": "72h"}
    And a run of the flow has started
    When the gate "approve" is resolved with {"approved": true}
    Then the run waited for gate "approve" with schema "review.json", acceptance condition "input.approved" and timeout 72h
    And the run completed node "approve" with the result {"approved": true}

  @REQ-FLOW-033 @id:FLOW-033-01
  Scenario: A subflow node runs as a child run
    Given a flow whose node "verify" of kind "subflow" has the node spec {"flow": "checks"}
    And the child run "checks" returns {"passed": 4}
    When a run of the flow starts with input {"tests": 4}
    Then the run started the child run "checks" with input {"tests": 4}
    And the run completed node "verify" with the result {"passed": 4}

  @REQ-FLOW-035 @id:FLOW-035-01
  Scenario: A failing handler fails the run and stops later nodes
    Given a flow whose node "plan" of kind "agent" has a registered node handler and precedes node "review" of kind "tool" with node spec {"tool": "t1"}
    And the node handler of "plan" returns the error "vendor_error"
    When a run of the flow starts with input {}
    Then the run fails with code "vendor_error"
    And the run made no step, timer, gate wait, child run or durable event

  @REQ-FLOW-035 @id:FLOW-035-02
  Scenario: An unclassified handler error fails the run with code internal
    Given a flow whose node "plan" of kind "agent" has a registered node handler
    And the node handler of "plan" returns an unclassified error "unexpected panic"
    When a run of the flow starts with input {}
    Then the run fails with code "internal"

  @REQ-FLOW-035 @id:FLOW-035-03
  Scenario Outline: A failing tool or task step fails the run with its error code
    Given a flow whose node "worker" of kind "<kind>" has the node spec {"<kind>": "w1"} and precedes node "after" of kind "tool" with node spec {"tool": "t2"}
    And the step "w1" fails with code "unavailable"
    When a run of the flow starts with input {}
    Then the run fails with code "unavailable"
    And the run made exactly one step, named "w1", with input {}

    Examples:
      | kind |
      | tool |
      | task |

  @REQ-FLOW-035 @id:FLOW-035-04
  Scenario: A gate node that times out fails the run with code timeout
    Given a flow whose node "approve" of kind "gate" has the node spec {"schema": "review.json", "accept_if": "input.approved", "timeout": "72h"}
    And a run of the flow has started
    When 72 hours pass without the gate "approve" being resolved
    Then the run fails with code "timeout"

  @REQ-FLOW-035 @id:FLOW-035-05
  Scenario: A failing subflow child run fails the run with its error code
    Given a flow whose node "verify" of kind "subflow" has the node spec {"flow": "checks"} and precedes node "after" of kind "tool" with node spec {"tool": "t2"}
    And the child run "checks" fails with code "unavailable"
    When a run of the flow starts with input {}
    Then the run fails with code "unavailable"
    And the run made no step

  @REQ-FLOW-036 @id:FLOW-036-01
  Scenario: Pure nodes and routing make no durable call
    Given a flow routing node "prep" of kind "set" to node "fast" of kind "set" setting "route" to "fast" when "state.n < 3", otherwise to node "slow" of kind "set" setting "route" to "slow"
    When a run of the flow starts with input {"n": 5}
    Then the run completes with state {"n": 5, "route": "slow"}
    And the run made no step, timer, gate wait, child run or durable event

  @REQ-FLOW-037 @id:FLOW-037-01
  Scenario: A replay makes the same durable calls in the same order
    Given a flow with node "a" of kind "tool" with node spec {"tool": "a"}, then node "b" of kind "wait" for "10s", then node "c" of kind "agent" whose handler calls step "c_step"
    And a first run recorded tool "a" failing with code "unavailable" 4 times then returning {"t": 1}, the 10s timer of "b" firing, and step "c_step" returning {"ok": true}
    When the run is replayed from its recorded history
    Then the replayed run made the same durable calls in the same order as the first run
    And the replayed run completed node "c" with the result {"ok": true}
    And step "c_step" was not re-executed during replay

  @REQ-FLOW-038 @id:FLOW-038-01
  Scenario: A completed run returns its final state and makes no further call
    Given a flow whose node "prep" of kind "set" sets "greeting" to "hi"
    When a run of the flow starts with input {}
    Then the run completes with state {"greeting": "hi"}
    And the run made no durable call after it completed

  @REQ-FLOW-044 @id:FLOW-044-01
  Scenario: Multiple commands in one superstep start before waiting for any to complete
    Given a flow that runs node "a" of kind "wait" for "10s" and node "b" of kind "wait" for "30s" in parallel
    When a run of the flow starts with input {}
    Then the run made a timer for 10s and a timer for 30s
    And the run is waiting

  @REQ-FLOW-050 @cost @id:FLOW-050-01
  Scenario: A cycle bounded only by a cost budget stops when the handlers' cost reaches it
    Given a flow whose node "verify" of kind "set" precedes node "repair" of kind "agent" with a registered node handler
    And node "repair" has a guard of 5000 milli-points and routes back to node "verify"
    And the node handler of "repair" returns {"fixed": true} with a settled cost of 2500 milli-points
    When a run of the flow starts with input {"failed": 1}
    Then node "repair" ran 2 times
    And the run fails with code "budget_exceeded" naming node "repair" and the cost budget 5000 milli-points

  @REQ-FLOW-051 @id:FLOW-051-01
  Scenario: A tool node with no input mapping receives the run's input
    Given a flow whose node "worker" of kind "tool" has no input mapping and the node spec {"tool": "w1"}
    And the step "w1" returns {"done": true}
    When a run of the flow starts with input {"a": 1}
    Then the run made exactly one step, named "w1", with input {"a": 1}

  @REQ-FLOW-052 @id:FLOW-052-01
  Scenario: The first failing future reported by Select decides the run's error
    Given a flow that runs node "a" of kind "tool" with node spec {"tool": "a"} and node "b" of kind "tool" with node spec {"tool": "b"} in parallel, then node "after" of kind "tool" with node spec {"tool": "t2"}
    And the step "a" fails with code "unavailable"
    And the step "b" fails with code "vendor_error"
    And Select reports the failed step "b" before the failed step "a"
    When a run of the flow starts with input {}
    Then the run fails with code "vendor_error"
    And the run awaited the failed step "b" but not the failed step "a"
    And the run made no step named "after"

  @REQ-FLOW-053 @id:FLOW-053-01
  Scenario: A failed handler is not followed by the other handler of the superstep
    Given a flow that runs node "first" of kind "agent" and node "second" of kind "agent" in parallel
    And the node handler of "first" returns the error "vendor_error"
    And the node handler of "second" returns {"ok": true}
    When a run of the flow starts with input {}
    Then the run fails with code "vendor_error"
    And the node handler of "second" was never called

  @REQ-FLOW-054 @id:FLOW-054-01
  Scenario: A definition that does not match the pinned hash fails the run before any durable call
    Given flow "review" is registered in the definition catalog with content hash "hash-v1"
    And the catalog returns flow "review" with content hash "hash-v2" for a run pinned to "hash-v1"
    When a run of "review" pinned to "hash-v1" starts with input {}
    Then the run fails with code "internal" naming flow "review"
    And the run made no step, timer, gate wait, child run or durable event

  @REQ-FLOW-055 @id:FLOW-055-01
  Scenario: A run with an empty pinned hash runs the definition the catalog resolves
    Given flow "review" version 1 whose node "done" of kind "set" sets "v" to 1 is registered in the definition catalog
    And flow "review" version 2 whose node "done" of kind "set" sets "v" to 2 is registered in the definition catalog
    When a run of "review" starts with an empty pinned hash and input {}
    Then the run completes with state {"v": 2}

  @REQ-FLOW-056 @id:FLOW-056-01
  Scenario Outline: A negative <field> fails the run before any durable call
    Given a flow whose node "slow" of kind "<kind>" has the node spec {"<field>": "<duration>"}
    When a run of the flow starts with input {}
    Then the run fails with code "invalid_argument" naming node "slow"
    And the run made no step, timer, gate wait, child run or durable event

    Examples:
      | kind | field    | duration |
      | wait | duration | -5s      |
      | gate | timeout  | -1h      |

  @REQ-FLOW-057 @id:FLOW-057-01
  Scenario: An unclassified handler error reports a generic failure and no error text
    Given a flow whose node "plan" of kind "agent" has a registered node handler
    And the node handler of "plan" returns an unclassified error "SEALED-CRITICAL-DETAIL"
    When a run of the flow starts with input {"secret": "input-value"}
    Then the run fails with code "internal"
    And the failure payload has code "internal" and message 'flow: node "plan" failed'
    And the failure payload does not contain "SEALED-CRITICAL-DETAIL"
    And the failure payload does not contain "input-value"

  @REQ-FLOW-058 @id:FLOW-058-01
  Scenario: A node visited twice in a loop issues two different idempotency keys
    Given a flow that visits node "worker" of kind "tool" with node spec {"tool": "w1"} twice in a loop guarded by at most 2 visits
    And the step "w1" returns {"ok": true}
    When a run of the flow starts with input {}
    Then the step "w1" was called twice
    And the two idempotency keys of step "w1" differ only in their superstep sequence

  @REQ-FLOW-058 @id:FLOW-058-02
  Scenario: Replaying a failed step reuses its idempotency key
    Given a flow whose node "worker" of kind "tool" with node spec {"tool": "w1"} is recorded with step "w1" failing once then returning {"ok": true}
    When the run is replayed from its recorded history
    Then the replayed step "w1" carried the same idempotency key as the failed first attempt

  @REQ-FLOW-059 @cost @id:FLOW-059-01
  Scenario: A tool step's completion carries no settled cost
    Given a flow that runs node "work" of kind "tool" with node spec {"tool": "w1"} before node "repair" of kind "agent" with a guard of 100 milli-points
    And the step "w1" returns {"ok": true}
    And the node handler of "repair" returns {"fixed": true} with a settled cost of 60 milli-points
    When a run of the flow starts with input {}
    Then the run completes with state {"ok": true, "fixed": true}
    And the step "w1" contributed 0 to the run's settled cost

  @REQ-FLOW-059 @cost @id:FLOW-059-02
  Scenario: A handler's result cost is reported on node_failed when it also returns an error
    Given a flow whose node "repair" of kind "agent" has a registered node handler
    And the node handler of "repair" returns {"fixed": true} with a settled cost of 2500 milli-points and the error "vendor_error"
    When a run of the flow starts with input {}
    Then the run fails with code "vendor_error"
    And the node "repair" failure reported a settled cost of 2500 milli-points

  @REQ-FLOW-060 @id:FLOW-060-01
  Scenario: WithMaxFanout above the default is clamped to the default
    Given a flow whose node "impl" of kind "map" fans out over 10001 items
    And a flow host built with WithMaxFanout(20000)
    When a run of the flow starts with input {}
    Then the run fails with code "budget_exceeded" naming node "impl", the item count 10001 and the fan-out limit 10000

  @REQ-FLOW-061 @id:FLOW-061-01
  Scenario: WithMaxFanout below one fails the run before any durable call
    Given a flow whose node "impl" of kind "map" fans out over 2 items
    And a flow host built with WithMaxFanout(0)
    When a run of the flow starts with input {}
    Then the run fails with code "invalid_argument"
    And the run made no step, timer, gate wait, child run or durable event
