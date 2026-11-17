@planned @wp:WP-L7-03 @runtime:temporal @level:component
Feature: A conversation resumed after a runtime restart loses no queued message

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-015 @id:CONV-015-01
  Scenario: Queued messages run after the runtime restarts
    Given ana sends "Write 100 paragraphs" and the turn is halfway
    And ana sends "One"
    And ana sends "Two"
    When the runtime process restarts
    And the first turn completes
    Then the turns ran in the order "Write 100 paragraphs", "One", "Two"
    And the coordinator run started 3 child runs

  @REQ-CONV-015 @id:CONV-015-02
  Scenario: A steering message survives the restart and redirects the turn
    Given ana sends "Write a long article about Rome" and the turn is halfway
    When ana steers "Make it about Florence instead"
    And the runtime process restarts
    And the turn completes
    Then the turn's child run received the user message "Make it about Florence instead"
    And the turn ends with state "completed"

  @REQ-CONV-015 @id:CONV-015-03
  Scenario: Restarting the runtime before a queued turn starts does not run it twice
    Given ana sends "Write 100 paragraphs" and the turn is halfway
    And ana sends "One"
    When the runtime process restarts before the turn of "One" starts
    And ana's message "One" is delivered a second time to the coordinator run
    Then the coordinator run started 2 child runs
    And the turns ran in the order "Write 100 paragraphs", "One"
