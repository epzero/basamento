@planned @wp:WP-L7-01 @runtime:any @level:component
Feature: Handoff to another agent

  Background:
    Given flow "team" defines agent node "triage" and agent node "fixer"

  @REQ-AGENT-016 @id:AGENT-016-01
  Scenario: An answer to a handoff request ends the run and names the target
    Given the scripted model of "triage" requests tool "search", then requests a handoff to agent "fixer" in 2 turns
    When an agent run of "triage" completes
    Then the agent run is completed with state "handoff"
    And the agent run output names node "fixer"
    And the agent run output holds model turns 1 and 2 in order

  @REQ-AGENT-017 @id:AGENT-017-01
  Scenario: The target starts with the turns the first agent carried
    Given the scripted model of "triage" requests a handoff to agent "fixer" in 1 turn
    And an agent run of "triage" completed with a handoff to node "fixer"
    When an agent run of "fixer" starts with that handoff
    Then the messages of model turn 1 of "fixer" contain model turn 1 of "triage"

  @REQ-AGENT-018 @id:AGENT-018-01
  Scenario: The target applies its own step cap
    Given agent "triage" has a step cap of 1
    And agent "fixer" has a step cap of 2
    And the scripted model of "triage" requests a handoff to agent "fixer" in 1 turn
    And the scripted model of "fixer" requests tool "search" in each of 3 turns after the handoff
    When the agent runs of "triage" and then "fixer" complete
    Then the agent run of "fixer" is completed with state "step_cap_reached"
    And the scripted model of "fixer" received 2 invocations

  @REQ-AGENT-018 @cost @id:AGENT-018-02
  Scenario: The target applies its own token cap
    Given agent "triage" has a token cap of 2000 tokens
    And agent "fixer" has a token cap of 10000 tokens
    And the scripted model of "triage" requests a handoff to agent "fixer" in 1 turn using 1000 input and 500 output tokens
    And the scripted model of "fixer" requests tool "search" in 2 turns after the handoff, each using 4000 input and 1000 output tokens, then answers "Fixed." in 1 turn using 1000 input and 100 output tokens
    When the agent runs of "triage" and then "fixer" complete
    Then the agent run of "fixer" runs its own model turns 1 and 2
    And the agent run of "fixer" is completed with state "token_cap_reached"

  @REQ-AGENT-018 @cost @id:AGENT-018-03
  Scenario: The target applies its own cost cap
    Given agent "triage" has a cost cap of 5000 milli-points
    And agent "fixer" has a cost cap of 1000 milli-points
    And the scripted model of "triage" requests a handoff to agent "fixer" in 1 turn costing 3000 milli-points
    And the scripted model of "fixer" requests tool "search" in 2 turns after the handoff, each costing 600 milli-points
    When the agent runs of "triage" and then "fixer" run
    Then the agent run of "triage" is completed with state "handoff"
    And the agent run of "fixer" is completed with state "cost_cap_reached"
    And the scripted model of "fixer" received 1 invocation

  @REQ-AGENT-019 @id:AGENT-019-01
  Scenario: The time cap covers the target from the start of the chain
    Given the run clock starts at "2026-11-17T10:00:00Z"
    And agent "triage" has a time cap of 1 minute
    And agent "fixer" has no time cap
    And the scripted model of "triage" requests a handoff to agent "fixer" in model turn 1, which takes 40 seconds on the run clock
    And the scripted model of "fixer" requests tool "search" in each of 2 turns after the handoff, and its model turn 1 takes 20 seconds on the run clock
    When the agent runs of "triage" and then "fixer" run
    Then the agent run of "triage" is completed with state "handoff" with started_at "2026-11-17T10:00:00Z" and time cap "1m"
    And the agent run of "fixer" is completed with state "time_cap_reached"
    And the scripted model of "fixer" received 1 invocation

  @REQ-AGENT-019 @id:AGENT-019-02
  Scenario: The target keeps running just under the time cap
    Given the run clock starts at "2026-11-17T10:00:00Z"
    And agent "triage" has a time cap of 1 minute
    And agent "fixer" has no time cap
    And the scripted model of "triage" requests a handoff to agent "fixer" in model turn 1, which takes 40 seconds on the run clock
    And the scripted model of "fixer" answers "Fixed." in 1 turn, which takes 19 seconds on the run clock
    When the agent runs of "triage" and then "fixer" complete
    Then the agent run of "triage" is completed with state "handoff" with started_at "2026-11-17T10:00:00Z" and time cap "1m"
    And the agent run of "fixer" is completed with output "Fixed."
    And the scripted model of "fixer" received 1 invocation

  @REQ-AGENT-019 @id:AGENT-019-03
  Scenario: The first agent's time cap governs a target with its own
    Given the run clock starts at "2026-11-17T10:00:00Z"
    And agent "triage" has a time cap of 1 minute
    And agent "fixer" has a time cap of 10 minutes
    And the scripted model of "triage" requests a handoff to agent "fixer" in model turn 1, which takes 40 seconds on the run clock
    And the scripted model of "fixer" requests tool "search" in each of 2 turns after the handoff, and its model turn 1 takes 20 seconds on the run clock
    When the agent runs of "triage" and then "fixer" run
    Then the agent run of "triage" is completed with state "handoff" with started_at "2026-11-17T10:00:00Z" and time cap "1m"
    And the agent run of "fixer" is completed with state "time_cap_reached"
    And the scripted model of "fixer" received 1 invocation

  @REQ-AGENT-016 @REQ-AGENT-019 @id:AGENT-019-04
  Scenario: A middle agent's handoff carries the cap it received
    Given the run clock starts at "2026-11-17T10:00:00Z"
    And agent "triage" has a time cap of 2 minutes
    And agent "fixer" has a time cap of 10 minutes
    And the scripted model of "triage" requests a handoff to agent "fixer" in model turn 1, which takes 10 seconds on the run clock
    And the scripted model of "fixer" requests a handoff to agent "triage" in model turn 1, which takes 10 seconds on the run clock
    When the agent runs of "triage" and then "fixer" run
    Then the agent run of "triage" is completed with state "handoff" with started_at "2026-11-17T10:00:00Z" and time cap "2m"
    And the agent run of "fixer" is completed with state "handoff" with started_at "2026-11-17T10:00:00Z" and time cap "2m"

  @REQ-AGENT-029 @id:AGENT-029-01
  Scenario: The target applies its own time cap when the first agent had none
    Given the run clock starts at "2026-11-17T10:00:00Z"
    And agent "triage" has no time cap
    And agent "fixer" has a time cap of 1 minute
    And the scripted model of "triage" requests a handoff to agent "fixer" in model turn 1, which takes 40 seconds on the run clock
    And the scripted model of "fixer" requests tool "search" in each of 2 turns after the handoff, and its model turn 1 takes 20 seconds on the run clock
    When the agent runs of "triage" and then "fixer" run
    Then the agent run of "triage" is completed with state "handoff" with started_at "2026-11-17T10:00:00Z" and no time cap
    And the agent run of "fixer" is completed with state "time_cap_reached"
    And the scripted model of "fixer" received 1 invocation

  @REQ-AGENT-029 @id:AGENT-029-02
  Scenario: A target under its own time cap completes
    Given the run clock starts at "2026-11-17T10:00:00Z"
    And agent "triage" has no time cap
    And agent "fixer" has a time cap of 1 minute
    And the scripted model of "triage" requests a handoff to agent "fixer" in model turn 1, which takes 40 seconds on the run clock
    And the scripted model of "fixer" requests tool "search" in 1 turn after the handoff, which takes 19 seconds on the run clock, then answers "Fixed." in 1 turn
    When the agent runs of "triage" and then "fixer" complete
    Then the agent run of "fixer" is completed with output "Fixed."
    And the scripted model of "fixer" received 2 invocations

  @REQ-AGENT-016 @id:AGENT-016-02
  Scenario: A turn that mixes a tool call with a handoff hands off without the tool call
    Given the scripted model of "triage" requests tools "search" and a handoff to agent "fixer" in 1 turn
    When an agent run of "triage" completes
    Then the agent run is completed with state "handoff"
    And the agent run output names node "fixer"
    And the agent run has 0 tool-call steps
    And tool "search" was invoked 0 times

  @REQ-AGENT-003 @id:AGENT-003-02
  Scenario: A handoff request is not a tool call
    Given the scripted model of "triage" requests tool "search", then requests a handoff to agent "fixer" in 2 turns
    When an agent run of "triage" completes
    Then the agent run is completed with state "handoff"
    And the agent run has 1 tool-call step
    And tool "handoff_to" was invoked 0 times

  @REQ-AGENT-016 @id:AGENT-016-03
  Scenario: The target starts with the turn that requested the handoff
    Given the scripted model of "triage" requests tool "search", then requests a handoff to agent "fixer" in 2 turns
    And an agent run of "triage" completed with a handoff to node "fixer"
    When an agent run of "fixer" starts with that handoff
    Then the messages of model turn 1 of "fixer" contain model turns 1 and 2 of "triage"
    And the messages of model turn 1 of "fixer" contain the request of tool "handoff_to" from model turn 2 of "triage"

  @REQ-AGENT-020 @id:AGENT-020-01
  Scenario: A handoff to an agent the flow does not define fails the run
    Given the scripted model of "triage" requests a handoff to agent "ghost" in 1 turn
    When an agent run of "triage" runs
    Then the agent run fails with code "not_found" naming agent "ghost"

  @REQ-AGENT-030 @id:AGENT-030-01
  Scenario: Multiple handoff requests in one turn fail the run
    Given the scripted model of "triage" requests handoffs to agents "fixer" and "reviewer" in 1 turn
    When an agent run of "triage" runs
    Then the agent run fails with code "invalid_argument"
    And the agent run has 0 tool-call steps
