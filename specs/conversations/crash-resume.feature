@planned @wp:WP-L7-04 @runtime:temporal @level:system
Feature: A conversation on the temporal runtime survives a crash
  The conversation logic does not depend on the runtime (WP-L7-03): on the
  temporal runtime, a turn interrupted by the process stopping is resumed
  after the process restarts on the same storage, and the queued messages
  sent before the stop run after it. A turn's model message is recorded and
  streamed once even when the turn's model call or recording step executes
  again. Reattach with a cursor, the reset before a retried attempt and the
  deltas of an attempt are specified in specs/streaming and are not repeated
  here.

  @REQ-CONV-022 @REQ-CONV-023 @REQ-STRM-002 @REQ-STRM-004 @id:CONV-022-01
  Scenario: A conversation resumes its turn after the process is killed, with no duplicate message
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow on the temporal runtime
    And ana sends "Tell me a story" and attaches to the stream
    And the scripted model streamed "Once upon" in 2 deltas
    When the process running conversation "c1" is killed and started again on the same data directory
    And ana reattaches with her last cursor
    And the scripted model streams "Once upon a time." in 3 deltas and the turn completes
    Then conversation "c1" holds these messages in order:
      | role  | text              |
      | user  | Tell me a story   |
      | model | Once upon a time. |
    And ana receives a reset for the message before the deltas of attempt 2
    And ana's stream contains message text "Once upon a time." exactly once

  @REQ-CONV-022 @id:CONV-022-02
  Scenario: Queued messages survive a restart and run after the resumed turn completes
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow on the temporal runtime
    And ana sends "Write a poem" and the turn starts
    And the scripted model streamed "Roses are" in 2 deltas
    And ana sends "Then translate it to French"
    And ana sends "And add a title"
    When the process running conversation "c1" is killed and started again on the same data directory
    And the scripted model answers "Roses are red." for turn 1, "Les roses sont rouges." for turn 2, and "La rose" for turn 3
    And all 3 turns complete
    Then conversation "c1" holds these messages in order:
      | role  | text                        |
      | user  | Write a poem                |
      | model | Roses are red.              |
      | user  | Then translate it to French |
      | model | Les roses sont rouges.      |
      | user  | And add a title             |
      | model | La rose                     |
