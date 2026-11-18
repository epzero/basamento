@planned @wp:WP-L7-03 @runtime:any @level:component
Feature: Cancelling a turn or a conversation

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-008 @id:CONV-008-01
  Scenario: A cancel message cancels the running turn
    Given ana sends "Write 100 paragraphs" and the turn starts
    When ana cancels the turn
    Then the turn's child run is cancelled
    And the coordinator run started 1 child run

  @REQ-CONV-009 @id:CONV-009-01
  Scenario: A cancelled turn keeps its partial message, marked interrupted
    Given ana sends "Write 100 paragraphs"
    And the scripted model streamed "Chapter one begins with" in 2 deltas
    When ana cancels the turn
    Then the turn ends with state "cancelled"
    And conversation "c1" contains the model message "Chapter one begins with" marked interrupted
    And ana receives the turn result with code "cancelled"

  @REQ-CONV-010 @id:CONV-010-01
  Scenario: Cancelling a turn keeps its queued messages
    Given ana sends "Write 100 paragraphs" and the turn is halfway
    And ana sends "Then summarise the paragraphs"
    When ana cancels the turn
    Then the turn of "Then summarise the paragraphs" starts after the cancelled turn
    And the coordinator run started 2 child runs

  @REQ-CONV-010 @id:CONV-010-02
  Scenario: The first cancelled turn's queue survives a second cancellation
    Given ana sends "Write 100 paragraphs" and the turn is halfway
    And ana sends "One"
    And ana sends "Two"
    When ana cancels the turn
    And ana cancels the turn of "One"
    Then the turn of "Two" starts
    And the turns ran in the order "Write 100 paragraphs", "One", "Two"

  @REQ-CONV-011 @id:CONV-011-01
  Scenario: Cancelling a conversation ends the running turn
    Given ana sends "Write 100 paragraphs" and the turn starts
    When ana cancels the conversation "c1"
    Then the coordinator run is cancelled
    And the turn's child run is cancelled

  @REQ-CONV-011 @id:CONV-011-02
  Scenario: Cancelling a conversation discards its queued messages
    Given ana sends "Write 100 paragraphs" and the turn is halfway
    And ana sends "One"
    And ana sends "Two"
    When ana cancels the conversation "c1"
    Then conversation "c1" has no queued message
    And the coordinator run started 1 child run

  @REQ-CONV-019 @id:CONV-019-01
  Scenario: A message sent after the conversation was cancelled is refused
    Given ana sent "Hello" and the turn completed
    And ana cancelled the conversation "c1"
    When ana sends "Are you there?"
    Then the send fails with code "conflict"
    And the coordinator run started 1 child run

  @REQ-CONV-021 @id:CONV-021-01
  Scenario: Cancelling a turn when no turn is running is refused
    Given ana sent "Hello" and the turn completed
    When ana cancels the turn
    Then the cancel fails with code "conflict"
    And conversation "c1" is still open
