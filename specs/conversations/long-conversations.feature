@planned @wp:WP-L7-03 @runtime:any @level:component
Feature: A long conversation keeps working
  The coordinator's durable history is bounded: on a runtime that supports
  continuing as new, it continues as new whenever the durable calls it has made
  itself since it last started (Await, Select, Step, Child, Sleep, Emit) reach
  its checkpoint threshold (1000 by default), checked at turn boundaries. The
  durable calls a child run makes inside itself do not count.

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-014 @id:CONV-014-01
  Scenario: The coordinator continues as new once its threshold is reached
    Given a runtime that supports continuing as new
    And the coordinator's checkpoint threshold is 7 durable calls
    And each turn of conversation "c1" is the coordinator's Await, Select, Step and Child, and 4 durable calls in the child run
    And 2 turns of conversation "c1" have completed
    When ana sends "Compare these three options"
    Then the coordinator continued as new once before the turn of "Compare these three options"
    And the coordinator run started 3 child runs, one per turn

  @REQ-CONV-014 @id:CONV-014-02
  Scenario: The checkpoint waits for the running turn to end
    Given a runtime that supports continuing as new
    And the coordinator's checkpoint threshold is 7 durable calls
    And ana sends "Write 100 paragraphs" and the coordinator has made its 8th durable call while the turn is still running
    And ana sends "Then summarise the paragraphs"
    When the first turn completes
    Then the coordinator continued as new once, only after the first turn ended
    And the second child run starts after the coordinator continued as new

  @REQ-CONV-014 @id:CONV-014-03
  Scenario: Turns run on after a second continuation
    Given a runtime that supports continuing as new
    And the coordinator's checkpoint threshold is 3 durable calls
    And each turn of conversation "c1" is the coordinator's Await, Select, Step and Child
    And 3 turns of conversation "c1" have completed
    When ana sends "One more question" and the turn completes
    Then the coordinator continued as new 4 times
    And the coordinator run is named "conv-c1"
    And the coordinator run started 4 child runs, one per turn

  @REQ-CONV-014 @id:CONV-014-04
  Scenario: On a runtime without continue-as-new the coordinator keeps running turns without checkpointing
    Given the runtime reports it does not continue as new
    And the coordinator's checkpoint threshold is 3 durable calls
    And each turn of conversation "c1" is the coordinator's Await, Select, Step and Child
    And 3 turns of conversation "c1" have completed
    When ana sends "One more question" and the turn completes
    Then the coordinator has not continued as new
    And the coordinator run started 4 child runs, one per turn

  @REQ-CONV-012 @REQ-CONV-014 @id:CONV-012-04
  Scenario: The history after a continuation reaches back to the first turn
    Given a runtime that supports continuing as new
    And ana sent "Hello" and the turn completed with the model message "Hi Ana"
    And the coordinator continued as new once
    When ana sends "What did I say first?" and the turn completes
    Then the scripted model's call for this turn records these texts in order:
      | Hello                 |
      | Hi Ana                |
      | What did I say first? |
