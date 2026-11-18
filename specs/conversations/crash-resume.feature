@planned @wp:WP-L7-04 @runtime:temporal @level:system
Feature: A conversation on the temporal runtime survives a restart
  The conversation logic does not depend on the runtime (WP-L7-03): on the
  temporal runtime, a turn stopped mid-way is replayed by the runtime from its
  history, and the turn record makes sure the turn's model message is recorded
  and streamed once. Detach and reattach are in specs/streaming and are not
  repeated here.

  @REQ-CONV-022 @id:CONV-022-01
  Scenario: A conversation resumes its turn after the process restarts, with no duplicate message
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow on the temporal runtime
    And ana sends "Tell me a story" and the turn starts
    And the scripted model streamed "Once upon a time" in 2 deltas
    When the process running conversation "c1" stops and starts again
    And the turn completes
    And ana reattaches with her last cursor
    Then conversation "c1" holds the user message "Tell me a story" once and the model message "Once upon a time" once
    And ana receives the model message "Once upon a time" exactly once
