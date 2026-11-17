@planned @wp:WP-L7-03 @runtime:any @level:component
Feature: Steering a running turn

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-006 @id:CONV-006-01
  Scenario: A steering message reaches the running turn at its next step boundary
    Given ana sends "Write a long article about Rome"
    When the turn finishes its first step
    And ana steers "Make it about Florence instead"
    Then the child run receives "Make it about Florence instead" at step 2
    And the coordinator run started 1 child run

  @REQ-CONV-006 @id:CONV-006-02
  Scenario: A steering message does not become a queued message
    Given ana sends "Write a long article about Rome" and the turn is halfway
    When ana steers "Make it about Florence instead"
    Then conversation "c1" has no queued message

  @REQ-CONV-007 @id:CONV-007-01
  Scenario: Steering keeps the partial message and continues the same turn
    Given ana sends "Write a long article about Rome"
    And the scripted model starts writing "Rome is a city of" as the first model message
    When the turn finishes its first step
    And ana steers "Make it about Florence instead"
    And the turn completes
    Then conversation "c1" keeps the first model message "Rome is a city of" as is
    And the turn was not cancelled
    And the turn's child run received the user message "Make it about Florence instead" after the first model message
    And no second child run started for the conversation

  @REQ-CONV-007 @id:CONV-007-02
  Scenario: A turn steered twice sees both messages in the order sent
    Given ana sends "Write a long article about Rome"
    When the turn finishes its first step
    And ana steers "Make it shorter"
    And the turn finishes its second step
    And ana steers "And about Florence instead"
    And the turn completes
    Then the turn's child run received the user message "Make it shorter" before "And about Florence instead"
    And the scripted model's third call recorded the user texts "Write a long article about Rome", "Make it shorter", "And about Florence instead", in that order
