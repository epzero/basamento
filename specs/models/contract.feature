@wp:WP-L3-01 @level:component
Feature: One invocation contract
  Chat, image, video and audio models are called the same way, whether their
  vendor streams one response, is polled, calls back or runs as a command.

  @REQ-MODEL-001 @contract @id:MODEL-001-01
  Scenario Outline: Every vendor protocol succeeds through the contract
    Given a simulated "<capability>" provider whose vendor protocol is "<protocol>"
    When a caller starts an invocation and follows its events
    Then the caller receives ordered events and then a result with state "succeeded"

    Examples:
      | capability     | protocol        |
      | chat           | streamed reply  |
      | embed          | single reply    |
      | image.generate | webhook         |
      | video.generate | submit and poll |
      | audio.tts      | streamed reply  |
      | audio.tts      | websocket       |
      | image.generate | command line    |

  @REQ-MODEL-001 @contract @id:MODEL-001-02
  Scenario: Cancelling an invocation in progress ends it as cancelled
    Given a simulated "video.generate" provider whose vendor protocol is "submit and poll"
    When a caller starts an invocation and cancels it after its first event
    Then the invocation ends with state "cancelled"

  @REQ-MODEL-002 @id:MODEL-002-01
  Scenario: A blocking call returns the final result
    Given a simulated "chat" provider that streams "Hello there" in 3 events
    When a caller invokes it and waits without following events
    Then the result message text is "Hello there"

  @REQ-MODEL-003 @id:MODEL-003-01
  Scenario: Followed events add up to the result
    Given a simulated "chat" provider that streams "Hello there" in 3 events
    When a caller starts an invocation and follows its events
    Then the caller receives 3 text events in order
    And the result message text is "Hello there"
