@planned @wp:WP-L7-03 @runtime:any @level:component
Feature: A failed turn leaves the conversation usable

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-013 @id:CONV-013-01
  Scenario: A failed turn keeps what it produced and marks it failed
    Given ana sends "Look up contract 42"
    And the scripted model streamed "Searching" in 2 deltas
    And the scripted model then makes the turn fail with code "vendor_error"
    When the turn ends
    Then the turn's child run ends with state "failed" and code "vendor_error"
    And conversation "c1" contains the model message "Searching" as interrupted in a turn marked failed

  @REQ-CONV-013 @id:CONV-013-02
  Scenario: The next message after a failed turn runs
    Given ana sent "Look up contract 42" and the turn failed with code "vendor_error"
    When ana sends "Summarise what you have"
    And the scripted model answers "Here is the summary"
    Then the coordinator run started 2 child runs
    And ana receives the model message "Here is the summary"

  @REQ-CONV-012 @REQ-CONV-013 @id:CONV-013-03
  Scenario: A later turn sees what a failed turn produced
    Given ana sends "Look up contract 42"
    And the scripted model answered "Searching" and then makes the turn fail with code "vendor_error"
    And the first turn ends
    When ana sends "Summarise what you have"
    Then the scripted model's call for the second turn records these texts in order:
      | Look up contract 42 |
      | Searching           |
      | Summarise what you have |
