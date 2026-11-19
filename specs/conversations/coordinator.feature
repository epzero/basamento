@wp:WP-L7-03 @runtime:any @level:component
Feature: Coordinating the turns of a conversation
  A conversation is one coordinator run that starts one child run per turn and
  never runs two turns at once. Streaming, detach and reattach are in
  specs/streaming and are not repeated here.

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-001 @id:CONV-001-01
  Scenario: One coordinator run serves every turn of a conversation
    When ana sends "Hello" and the turn completes
    And ana sends "And how are you?" and the turn completes
    Then conversation "c1" has one coordinator run, named "conv-c1"
    And the coordinator run started 2 child runs, one per turn

  @REQ-CONV-002 @id:CONV-002-01
  Scenario: The next turn waits until the running turn has ended
    Given ana sends "Plan a trip to Rome" and the turn is halfway
    And ana sends "Book the first option"
    When the first turn completes
    Then the coordinator run started 2 child runs, the second after the first ended

  @REQ-CONV-002 @id:CONV-002-02
  Scenario: A failed turn does not block the next one
    Given ana sends "Summarise the contract" and the turn starts
    And the scripted model makes the turn fail
    And ana sends "Try again, in one sentence"
    When the failed turn ends
    Then the second child run starts
