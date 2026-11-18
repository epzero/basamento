@planned @wp:WP-L7-04 @runtime:any @level:component
Feature: A turn's messages are recorded once
  A durable runtime may execute a turn's model call or its message-recording
  step again, after a restart or a retry. The conversation history then holds
  each message of the turn once, in order.

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-023 @id:CONV-023-01
  Scenario: A recording step that runs again does not append a second message
    Given ana sends "Tell me a story"
    And the scripted model streamed "Once upon a time" in 2 deltas
    And the turn completes with the model message "Once upon a time"
    When the recording step of the first turn runs again for the same model message
    Then conversation "c1" holds these messages in order:
      | role  | text             |
      | user  | Tell me a story  |
      | model | Once upon a time |
