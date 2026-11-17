@planned @wp:WP-L4-04 @level:component
Feature: The flow host
  The flow host is the one durable entrypoint (ADR-0011). It runs the flow
  definition a run was started with, drives the pure flow machine through the
  durable context of the run, turns each command of the machine into the
  matching durable call, and dispatches the effectful nodes whose kind has a
  registered node handler to that handler. These scenarios run the flow host
  against a recording fake of the durable context, so a run's durable calls
  can be read back exactly.

  Background:
    Given a recording fake runtime with the durable context sealed after its first snapshot

  @REQ-FLOW-027 @id:FLOW-027-01
  Scenario: A run keeps its pinned definition when another version is registered
    Given flow "review" version 1 whose node "plan" of kind "agent" has a registered node handler
    And flow "review" version 1 is registered
    And a run of "review" version 1 starts and waits at the node "plan"
    When flow "review" version 2, whose node "plan" is of kind "agent" with no registered node handler, is registered
    And the node handler of "plan" returns {"ok": true}
    Then the run completes with state plan = {"ok": true}

  @REQ-FLOW-028 @id:FLOW-028-01
  Scenario Outline: A node kind without a registered handler fails the run before any node runs
    Given flow "triage" whose node "prep" of kind "set" precedes the node "worker" of kind "<kind>"
    And no node handler is registered for the kind "<kind>"
    When a run of "triage" starts with input {}
    Then the run fails with code "unsupported" naming node "worker"
    And the run made no step, timer, gate wait, child run or durable event

    Examples:
      | kind  |
      | model |
      | agent |
      | tool  |
      | task  |

  @REQ-FLOW-029 @REQ-FLOW-030 @id:FLOW-029-01
  Scenario Outline: Every effectful node kind with a registered handler runs as one step
    Given flow "triage" whose node "worker" of kind "<kind>" has a registered node handler
    And the node "worker" maps in the input from "state.draft" and has the node spec {"<kind>": "w1"}
    When a run of "triage" starts with input {"draft": "hello"}
    Then the run made exactly one step, named "worker", with input {"draft": "hello"}
    And the run completes with state worker = {"said": "done"}
    Examples:
      | kind  |
      | model |
      | agent |
      | tool  |
      | task  |

  @REQ-FLOW-031 @id:FLOW-031-01
  Scenario: A wait node waits for its duration
    Given a flow whose node "cool_down" of kind "wait" has the node spec {"duration": "30s"}
    When a run of the flow starts
    And 30 seconds pass
    And 1 second passes
    Then the run made a timer for 30s
    And the run completed node "cool_down" with no result

  @REQ-FLOW-031 @id:FLOW-031-02
  Scenario: A wait node is not complete before its duration passes
    Given a flow whose node "cool_down" of kind "wait" has the node spec {"duration": "30s"}
    When a run of the flow starts
    And 29 seconds pass
    Then the run made a timer for 30s
    And the run is waiting

  @REQ-FLOW-032 @id:FLOW-032-01
  Scenario: A gate node waits for the gate to be resolved
    Given a flow whose node "approve" of kind "gate" has the node spec {"schema": "review.json", "accept_if": "input.approved", "timeout": "72h"}
    When a run of the flow starts
    And the gate "approve" is resolved with {"approved": true}
    Then the run waited for gate "approve" with schema "review.json", acceptance condition "input.approved" and timeout 72h
    And the run completed node "approve" with the result {"approved": true}

  @REQ-FLOW-033 @id:FLOW-033-01
  Scenario: A subflow node runs as a child run
    Given a flow whose node "verify" of kind "subflow" has the node spec {"flow": "checks"}
    When a run of the flow starts with input {"tests": 4}
    Then the run started the child run "checks" with input {"tests": 4}
    And the run completed node "verify" with the result {"passed": 4}

  @REQ-FLOW-034 @id:FLOW-034-01
  Scenario: A completed node emits its durable event
    Given a flow whose node "plan" of kind "agent" has a registered node handler
    And the node handler of "plan" returns {"steps": 3}
    When a run of the flow starts
    Then the run emitted a durable event "node_completed" with data {"node": "plan", "result": {"steps": 3}}

  @REQ-FLOW-034 @id:FLOW-034-02
  Scenario: A completed pure node emits its durable event too
    Given a flow whose node "prep" of kind "set" sets "greeting" to "hi"
    When a run of the flow starts
    Then the run emitted a durable event "node_completed" with data {"node": "prep", "result": {"greeting": "hi"}}
    And the run made no step

  @REQ-FLOW-035 @id:FLOW-035-01
  Scenario: A handler that fails makes its node fail
    Given a flow routing node "plan" to node "review" by the default edge and to node "repair" on a failure
    And the node "plan" of kind "agent" has a registered node handler
    And the node handler of "plan" returns the error "vendor_error"
    And the node "repair" of kind "agent" has a registered node handler that returns {"error": "vendor_error"}
    When a run of the flow starts
    Then the run went to node "repair" on the failure of node "plan"
    And the run completed node "repair" with the result {"error": "vendor_error"}

  @REQ-FLOW-035 @id:FLOW-035-02
  Scenario: A failing handler with no failure edge fails the run
    Given a flow routing node "plan" to node "review" by the default edge and to no node on a failure
    And the node "plan" of kind "agent" has a registered node handler
    And the node handler of "plan" returns the error "vendor_error"
    When a run of the flow starts
    Then the run fails with code "vendor_error"

  @REQ-FLOW-036 @id:FLOW-036-01
  Scenario: Pure nodes and routing make no durable call
    Given a flow routing node "prep" of kind "set" to node "fast" when "state.n < 3", otherwise to node "slow"
    When a run of the flow starts with input {"n": 5}
    Then the run went to node "slow"
    And the run made no step, timer, gate wait, child run or durable event

  @REQ-FLOW-037 @id:FLOW-037-01
  Scenario: A replay makes the same durable calls in the same order
    Given a flow with node "a" of kind "tool", then node "b" of kind "wait", then node "c" of kind "agent"
    And the node "c" has a registered node handler
    And the recorded history of the run has the tool "a" failed with code "unavailable" 4 times, then its result {"t": 1}, the timer of "b" fired and the node handler of "c" returned {"ok": true}
    When a run of the flow starts
    And the run is replayed from its recorded history
    Then the replayed run made the same durable calls in the same order as the first run
    And the replayed run completed node "c" with the result {"ok": true}
    And the node handler of node "c" was run once

  @REQ-FLOW-038 @id:FLOW-038-01
  Scenario: A completed run returns its final state and makes no further call
    Given a flow whose node "prep" of kind "set" sets "greeting" to "hi"
    When a run of the flow starts with input {}
    Then the run completes with state {"greeting": "hi"}
    And the run made no durable call after it completed
