@wp:WP-L7-03 @runtime:any @level:component
Feature: Queuing messages sent during a turn

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-003 @id:CONV-003-01
  Scenario: A message sent during a turn waits as a queued message
    Given ana sends "Plan a trip to Rome" and the turn is halfway
    When ana sends "Book the first option"
    Then conversation "c1" has 1 queued message, "Book the first option"
    And exactly 1 child run has started

  @REQ-CONV-004 @id:CONV-004-01
  Scenario: Queued messages run in the order they were sent
    Given ana sends "Write three names" and the turn is halfway
    And ana sends "One"
    And ana sends "Two"
    And ana sends "Three"
    When the first turn completes
    Then the turns start in the order "Write three names", "One", "Two", "Three"

  @REQ-CONV-004 @id:CONV-004-02
  Scenario: A message sent between turns starts the next turn
    Given ana sends "Hello" and the turn completes
    When ana sends "Show me the file list"
    Then the turn of "Show me the file list" starts at once
    And conversation "c1" has no queued message

  @REQ-CONV-005 @id:CONV-005-01
  Scenario: A duplicate queued message starts one turn
    Given ana sends "Plan a trip to Rome" and the turn is halfway
    And ana sends "Book the first option"
    And ana sends "Book the first option" again
    When the first turn completes
    Then the turns start in the order "Plan a trip to Rome", "Book the first option"
    And the coordinator run started 2 child runs

  @REQ-CONV-005 @id:CONV-005-02
  Scenario: A duplicate message delivered to the coordinator is run once
    Given ana sends "Plan a trip to Rome" and the turn is halfway
    When ana's message "Plan a trip to Rome" is delivered a second time
    Then the coordinator run started 1 child run
    And conversation "c1" has no queued message

  @REQ-CONV-005 @id:CONV-005-03
  Scenario: The same text sent twice in a row is two messages
    Given ana sends "Hello" and the turn completes
    When ana sends "Hello"
    Then the scripted model's call for this turn records 3 messages, in order
    And the scripted model's call for this turn recorded the user text "Hello" twice
