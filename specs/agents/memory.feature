@planned @wp:WP-L7-01 @runtime:any @level:component
Feature: Memory policy: what goes into the next turn

  Background:
    Given flow "worker" defines agent node "worker" with a scripted model

  @REQ-AGENT-023 @id:AGENT-023-01
  Scenario: The start of the history is kept
    Given agent "worker" has a system message "You are a reviewer." and an input token limit of 1000 tokens
    And the scripted model of "worker" requests tool "search" in each of 3 turns
    When an agent run of "worker" reaches model turn 4 with messages of 3500 tokens
    Then the messages of model turn 4 contain the system message "You are a reviewer."
    And the messages of model turn 4 contain model turn 1

  @REQ-AGENT-024 @id:AGENT-024-01
  Scenario: The latest whole turns are kept
    Given agent "worker" has no system message and an input token limit of 1000 tokens
    And the scripted model of "worker" requests tool "search" in each of 3 turns
    When an agent run of "worker" reaches model turn 4 with messages of 3500 tokens
    Then the messages of model turn 4 contain model turn 1, which uses 1000 tokens
    And the messages of model turn 4 contain model turn 3
    And the messages of model turn 4 do not contain model turn 2

  @REQ-AGENT-025 @id:AGENT-025-01
  Scenario: A dropped request takes the tool result with it
    Given agent "worker" has no system message and an input token limit of 1000 tokens
    And the scripted model of "worker" requests tool "search" in each of 3 turns
    When an agent run of "worker" reaches model turn 4 with messages of 3500 tokens
    Then the messages of model turn 4 contain no request for tool "search" from model turn 2
    And the messages of model turn 4 contain no result of tool "search" that followed model turn 2

  @REQ-AGENT-026 @id:AGENT-026-01
  Scenario: The dropped message count is recorded with the turn
    Given agent "worker" has no system message and an input token limit of 1000 tokens
    And the scripted model of "worker" requests tool "search" in each of 3 turns
    When an agent run of "worker" reaches model turn 4 with messages of 3500 tokens
    Then the result of the model turn 4 step records 3 dropped messages

  @REQ-AGENT-024 @id:AGENT-024-02
  Scenario: Messages within the limit are kept whole
    Given agent "worker" has no system message and an input token limit of 1000 tokens
    And the scripted model of "worker" requests tool "search" in each of 2 turns
    When an agent run of "worker" reaches model turn 3 with messages of 900 tokens
    Then the messages of model turn 3 contain model turns 1 and 2
    And the result of the model turn 3 step records 0 dropped messages

  @REQ-AGENT-027 @id:AGENT-027-01
  Scenario: Kept messages over the limit fail the run
    Given agent "worker" has a system message "You are a reviewer." and an input token limit of 1000 tokens
    And agent "worker"'s system message uses 1200 tokens
    And the scripted model of "worker" requests tool "search" in each of 3 turns
    When an agent run of "worker" reaches model turn 4
    Then the agent run fails with code "invalid_argument"
    And the scripted model received 3 invocations
