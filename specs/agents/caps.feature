@wp:WP-L7-01 @runtime:any @level:component
Feature: Caps on steps, cost, tokens and time

  Background:
    Given the run clock starts at "2026-11-17T10:00:00Z"

  @REQ-AGENT-008 @id:AGENT-008-01
  Scenario: The step cap stops the loop before the extra turn
    Given agent "worker" has a step cap of 2
    And the scripted model of "worker" requests tool "search" in each of 3 turns
    When an agent run of "worker" runs
    Then the agent run is completed with state "step_cap_reached"
    And the scripted model received 2 invocations
    And the agent run output holds model turns 1 and 2 in order

  @REQ-AGENT-009 @cost @id:AGENT-009-01
  Scenario: A turn is reserved before it starts
    Given agent "worker" has a cost cap of 1000 milli-points
    And the scripted model of "worker" answers "Done." in 1 turn with an estimated cost of 500 milli-points and an actual cost of 400 milli-points
    When an agent run of "worker" completes
    Then 500 milli-points were reserved before the start of model turn 1
    And model turn 1 was charged 400 milli-points and its reservation was closed

  @REQ-AGENT-010 @cost @id:AGENT-010-01
  Scenario: The cost cap stops the loop before a turn over it
    Given agent "worker" has a cost cap of 1000 milli-points
    And the scripted model of "worker" requests tool "search" in 2 turns, each reserving and costing 600 milli-points
    When an agent run of "worker" runs
    Then the agent run is completed with state "cost_cap_reached"
    And the scripted model received 1 invocation
    And the agent run output holds only model turn 1

  @REQ-AGENT-009 @REQ-AGENT-010 @cost @id:AGENT-009-02
  Scenario: Spending exactly the cost cap does not stop the loop
    Given agent "worker" has a cost cap of 1000 milli-points
    And the scripted model of "worker" requests tool "search" in model turn 1 costing 1000 milli-points, then answers "Done." in 1 turn costing 0
    When an agent run of "worker" completes
    Then the scripted model received 2 invocations
    And the agent run is completed with output "Done."

  @REQ-AGENT-011 @cost @id:AGENT-011-01
  Scenario: The token cap stops the loop before a turn over it
    Given agent "worker" has a token cap of 10000 tokens
    And the scripted model of "worker" requests tool "search" in 2 turns, each turn using 5000 input and 1000 output tokens
    When an agent run of "worker" runs
    Then the agent run is completed with state "token_cap_reached"
    And the scripted model received 1 invocation
    And the agent run output holds only model turn 1

  @REQ-AGENT-011 @cost @id:AGENT-011-02
  Scenario: Using exactly the token cap does not stop the loop
    Given agent "worker" has a token cap of 10000 tokens
    And the scripted model of "worker" requests tool "search" in model turn 1 using 5000 input and 5000 output tokens, then answers "Done." in 1 turn costing no tokens
    When an agent run of "worker" completes
    Then the scripted model received 2 invocations
    And the agent run is completed with output "Done."

  @REQ-AGENT-012 @id:AGENT-012-01
  Scenario: The time cap stops the loop before the next turn
    Given agent "worker" has a time cap of 1 minute
    And the scripted model of "worker" requests tool "search" in each of 2 turns, and the tool call of model turn 1 takes 60 seconds on the run clock
    When an agent run of "worker" runs
    Then the agent run is completed with state "time_cap_reached"
    And the scripted model received 1 invocation
    And the agent run output holds only model turn 1

  @REQ-AGENT-012 @id:AGENT-012-02
  Scenario: A run under its time cap keeps running
    Given agent "worker" has a time cap of 1 minute
    And the scripted model of "worker" requests tool "search", then answers "Done." in 2 turns, and the tool call of model turn 1 takes 59 seconds on the run clock
    When an agent run of "worker" completes
    Then the agent run is completed with output "Done."
    And the scripted model received 2 invocations

  @planned @wp:WP-L7-01 @REQ-AGENT-012 @id:AGENT-012-03
  Scenario: The time cap is not deferred to an amortized check boundary
    Given agent "worker" has a time cap of 1 minute
    And agent "worker" checks its caps every 3 turns
    And the scripted model of "worker" requests tool "search" in each of 3 turns, and the tool call of model turn 1 takes 60 seconds on the run clock
    When an agent run of "worker" runs
    Then the agent run is completed with state "time_cap_reached"
    And the scripted model received 1 invocation
    And the agent run output holds only model turn 1

  @REQ-AGENT-028 @id:AGENT-028-01
  Scenario: Cancelling between turns starts no further turn
    Given the scripted model of "worker" requests tool "search", then answers "Done." in 2 turns
    And an agent run of "worker" has completed model turn 1 and its tool call
    When the agent run is cancelled before model turn 2 starts
    Then the agent run fails with code "cancelled"
    And the scripted model received 1 invocation
