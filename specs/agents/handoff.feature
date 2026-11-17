@planned @wp:WP-L7-01 @runtime:any @level:component
Feature: Handoff to another agent

  Background:
    Given flow "team" defines agent node "triage" looped back to itself, and agent node "fixer"

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
    And the scripted model of "fixer" requests tool "search" in 2 turns after the handoff
    When the agent runs of "triage" and then "fixer" complete
    Then the agent run of "fixer" is completed with state "step_cap_reached"
    And the scripted model of "fixer" received 2 invocations

  @REQ-AGENT-018 @REQ-AGENT-015 @id:AGENT-018-02
  Scenario: The target applies its own token cap
    Given agent "triage" has a cost cap of 10000 milli-points
    And agent "fixer" has a token cap of 10000 tokens
    And the scripted model of "triage" requests a handoff to agent "fixer" in 1 turn using 500 input and 500 output tokens
    And the scripted model of "fixer" requests tool "search" in 2 turns after the handoff, each using 5000 input and 1000 output tokens
    When the agent runs of "triage" and then "fixer" complete
    Then the agent run of "fixer" runs its own model turns 1 and 2
    And the agent run of "fixer" is completed with state "token_cap_reached"

  @REQ-AGENT-019 @REQ-AGENT-015 @id:AGENT-019-01
  Scenario: The time cap covers the target
    Given agent "triage" has a time cap of 1 minute
    And agent "fixer" has no time cap
    And the scripted model of "triage" requests a handoff to agent "fixer" in 1 turn, and model turn 2 of "fixer" starts on the run clock after 60 seconds
    When the agent runs of "triage" and then "fixer" run
    Then the agent run of "fixer" is completed with state "time_cap_reached"
    And the scripted model of "fixer" received 1 invocation

  @REQ-AGENT-020 @id:AGENT-020-01
  Scenario: A handoff to an agent the flow does not define fails the run
    Given the scripted model of "triage" requests a handoff to agent "ghost" in 1 turn
    When an agent run of "triage" runs
    Then the agent run fails with code "not_found" naming agent "ghost"

  @REQ-AGENT-021 @cost @id:AGENT-021-01
  Scenario: Carried turns that no cap of the target fits fail its run
    Given agent "triage" has a step cap of 4
    And agent "fixer" has a token cap of 5000 tokens
    And the scripted model of "triage" requests tool "search" in each of 3 turns, each using 3000 input and 1000 output tokens, and then requests a handoff to agent "fixer"
    And the agent run of "triage" completed with a handoff to node "fixer"
    When an agent run of "fixer" starts with that handoff
    Then the agent run of "fixer" fails with code "invalid_argument"
    And the scripted model of "fixer" received 0 invocations

  @REQ-AGENT-022 @id:AGENT-022-01
  Scenario: A handoff loop fails at the second return
    Given agent "fixer" hands back to agent "triage", and agent "triage" hands to agent "fixer" again
    When the agent runs of "triage" and then "fixer" start
    Then the second agent run of "triage" fails with code "conflict"

  @REQ-AGENT-017 @id:AGENT-017-02
  Scenario: The carried turns count as the target's own
    Given the scripted model of "triage" requests tool "search" in model turn 1, then requests a handoff to agent "fixer" in model turn 2
    And the messages of "triage" after model turn 2 equal the carried turns
    When an agent run of "fixer" starts with that handoff and its model turn 1 answers "Fixed."
    Then model turn 1 of "fixer" was made with the carried turns in its message history
