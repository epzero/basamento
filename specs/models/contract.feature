@planned @wp:WP-L3-01 @level:component
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

  @REQ-MODEL-088 @planned @wp:WP-L3-01 @id:MODEL-088-01
  Scenario: An ended invocation stays readable inside the retention window
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a registry with a retention window of 1 hour
    And a caller starts an invocation with client ref "ref-1" that streams "Hello" in 2 events and ends as succeeded
    When 59 minutes pass
    Then the lookup of "ref-1" finds its invocation
    And following its events from the start yields 2 text events
    And its result has state "succeeded"

  @REQ-MODEL-088 @planned @wp:WP-L3-01 @id:MODEL-088-02
  Scenario: An ended invocation is gone once the retention window has passed
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a registry with a retention window of 1 hour
    And a caller starts an invocation with client ref "ref-1" that ends as succeeded
    When 1 hour passes
    Then the lookup of "ref-1" finds no invocation
    And following its events fails with code "not_found"
    And waiting for its result fails with code "not_found"
    And cancelling it fails with code "not_found"

  @REQ-MODEL-088 @planned @wp:WP-L3-01 @id:MODEL-088-03
  Scenario: A start with an evicted client ref starts a new invocation
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a registry with a retention window of 1 hour
    And a caller starts an invocation with client ref "ref-1" that ends as succeeded
    When 1 hour passes
    And a caller starts an invocation with client ref "ref-1" that ends as succeeded
    Then the provider accepted 2 invocations
    And the lookup of "ref-1" finds its invocation

  @REQ-MODEL-088 @planned @wp:WP-L3-01 @id:MODEL-088-04
  Scenario: The ended bound evicts the oldest ended invocation
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a registry that keeps at most 2 ended invocations
    And a caller starts an invocation with client ref "ref-1" that ends as succeeded
    And 1 minute passes
    And a caller starts an invocation with client ref "ref-2" that ends as succeeded
    And 1 minute passes
    When a caller starts an invocation with client ref "ref-3" that ends as succeeded
    Then the lookup of "ref-1" finds no invocation
    And the lookup of "ref-2" finds its invocation
    And the lookup of "ref-3" finds its invocation

  @REQ-MODEL-088 @planned @wp:WP-L3-01 @id:MODEL-088-05
  Scenario: A running invocation is never evicted
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a registry with a retention window of 1 hour and an ended bound of 2
    And a caller starts an invocation with client ref "ref-1" that stays running
    When 24 hours pass
    And 3 other invocations start and end
    Then the lookup of "ref-1" finds its invocation
    When the invocation with client ref "ref-1" ends as succeeded
    Then its result has state "succeeded"
    And the lookup of "ref-1" finds its invocation
