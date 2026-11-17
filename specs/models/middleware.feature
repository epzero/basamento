@planned @wp:WP-L3-02 @level:component
Feature: Middleware around every chat call
  A chat call goes through the call log, the recording, the fallback, the
  metering, the retry middleware and the timeouts, in that order: logging and
  recording stay outer, so a retry costs two log entries but is one
  invocation.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"

  @REQ-MODEL-048 @id:MODEL-048-01
  Scenario: A provider error that may pass is tried again
    Given a scripted chat model that fails attempt 1 with code "vendor_error" marked retryable and streams "Hello" on later attempts
    When a caller invokes "sim-chat"
    Then the call succeeded with message text "Hello"
    And the call made 2 attempts

  @REQ-MODEL-049 @id:MODEL-049-01
  Scenario Outline: An error that may not pass ends the call
    Given a scripted chat model that fails attempt 1 with a "<error>" error that is not retryable
    When a caller invokes "sim-chat"
    Then the call failed with code "<code>"
    And the call made 1 attempt
    And the provider accepted 1 invocation

    Examples:
      | error                | code             |
      | vendor request       | vendor_error     |
      | authentication       | unauthenticated  |
      | invalid argument     | invalid_argument |
      | budget               | budget_exceeded  |
      | cancelled attempt    | cancelled        |
      | rate limit by itself | rate_limited     |

  @REQ-MODEL-050 @id:MODEL-050-01
  Scenario: A failure after the first delta is not retried
    Given a scripted chat model that streams 2 deltas and then fails attempt 1 with code "vendor_error" marked retryable
    When a caller starts an invocation and follows its events
    Then the caller receives exactly 2 text events
    And the call failed with code "vendor_error"
    And the call made 1 attempt
    And the caller receives no "reset" event

  @REQ-MODEL-051 @id:MODEL-051-01
  Scenario: Waits double from one second, with seeded jitter
    Given a scripted chat model that fails its first 2 attempts with code "unavailable" marked retryable, whose last error carries no Retry-After
    And the call's seeded randomness makes the scripted model 0
    When a caller invokes "sim-chat"
    Then invocation 2 started 1000 milliseconds after invocation 1 ended
    And invocation 3 started 2000 milliseconds after invocation 2 ended

  @REQ-MODEL-051 @cost @id:MODEL-051-02
  Scenario: The jitter comes from the call's seed, so each seed gives its own waits
    Given a scripted chat model that fails attempt 1 with code "unavailable" marked retryable, whose last error carries no Retry-After
    When a caller invokes "sim-chat" with seeded randomness that makes the scripted model 500
    Then invocation 2 started 1500 milliseconds after invocation 1 ended

  @REQ-MODEL-052 @id:MODEL-052-01
  Scenario: A Retry-After longer than the computed wait is honoured
    Given a scripted chat model that fails attempt 1 with code "rate_limited" marked retryable and a Retry-After of 5 seconds
    When a caller invokes "sim-chat"
    Then invocation 2 started 5000 milliseconds after invocation 1 ended

  @REQ-MODEL-052 @id:MODEL-052-02
  Scenario: A Retry-After shorter than the computed wait is not used
    Given a scripted chat model that fails attempt 1 with code "rate_limited" marked retryable and a Retry-After of 500 milliseconds
    And the call's seeded randomness makes the scripted model 0
    When a caller invokes "sim-chat"
    Then invocation 2 started 1000 milliseconds after invocation 1 ended

  @REQ-MODEL-053 @cost @id:MODEL-053-01
  Scenario: A wait that reaches the total timeout fails the call
    Given a scripted chat model that fails attempt 1 with code "unavailable" marked retryable and a Retry-After of 5 seconds
    When a caller invokes "sim-chat" with a total timeout of 3 seconds
    Then the call failed with code "timeout"
    And the call made 1 attempt

  @REQ-MODEL-053 @cost @id:MODEL-053-02
  Scenario: A total timeout ends the ladder of structured output
    Given chat model "sim-chat" whose output schema requires an integer field "answer"
    And chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And a scripted chat model that fails attempt 1 with code "timeout" marked retryable and a Retry-After of 2 seconds
    When a call asks "sim-chat" for output that satisfies the output schema with a total timeout of 3 seconds
    Then the call failed with code "timeout"
    And the call made 1 attempt

  @REQ-MODEL-055 @id:MODEL-055-01
  Scenario: A new attempt starts with a reset naming it
    Given a scripted chat model that fails attempt 1 with code "unavailable" marked retryable and streams "Hi" in 2 deltas on later attempts
    When a caller starts an invocation and follows its events
    Then the caller receives a "reset" event for attempt 2 after attempt 1's error
    And the caller receives exactly 2 text events, all of attempt 2
    And the result message text is "Hi"

  @REQ-MODEL-056 @cost @id:MODEL-056-01
  Scenario: An attempt that never starts streaming times out at the first-chunk timeout
    Given a scripted chat model with a first-chunk timeout of 10 seconds that never produces a first event
    When a caller starts an invocation and follows its events
    And 10 seconds pass
    Then attempt 1 was cancelled with code "timeout"
    And the call made 3 attempts

  @REQ-MODEL-057 @cost @id:MODEL-057-01
  Scenario: An attempt that falls quiet times out at the idle timeout
    Given a scripted chat model with an idle timeout of 30 seconds that streams 1 delta and then falls quiet
    When a caller starts an invocation and follows its events
    And 30 seconds pass
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-058 @cost @id:MODEL-058-01
  Scenario: A stream that keeps talking is stopped at the total timeout
    Given a scripted chat model with a total timeout of 30 seconds that streams 1 delta every 5 seconds
    When a caller starts an invocation and follows its events
    And 30 seconds pass
    Then attempt 1 was cancelled with code "timeout"
    And the caller received 6 text events

  @REQ-MODEL-059 @cost @id:MODEL-059-01
  Scenario: The defaults time out a silent stream at 30 seconds
    Given a scripted chat model that never produces a first event
    When a caller starts an invocation and follows its events
    And 29 seconds pass
    Then attempt 1 is still running
    When 1 second passes
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-059 @cost @id:MODEL-059-02
  Scenario: The defaults time out a quiet stream at 60 seconds
    Given a scripted chat model that streams 1 delta and then falls quiet
    When a caller starts an invocation and follows its events
    And 59 seconds pass
    Then attempt 1 is still running
    When 1 second passes
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-059 @cost @id:MODEL-059-03
  Scenario: The defaults stop a long stream at 10 minutes
    Given a scripted chat model that streams 1 delta every 5 seconds
    When a caller starts an invocation and follows its events
    And 9 minutes 59 seconds pass
    Then attempt 1 is still running
    When 1 second passes
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-060 @cost @id:MODEL-060-01
  Scenario: Every attempt that reached the provider is metered
    Given a scripted chat model that fails attempt 1 with code "vendor_error" marked retryable, using 100 input tokens and 0 output tokens
    And the scripted model streams "Hello" on attempt 2, using 200 input tokens and 50 output tokens
    When a caller invokes "sim-chat"
    Then the metering recorded 300 input tokens and 50 output tokens
    And the metering recorded attempt 1 at 300 milli-points and attempt 2 at 700 milli-points

  @REQ-MODEL-061 @cost @id:MODEL-061-01
  Scenario: An attempt the provider does not bill costs nothing
    Given a scripted chat model that fails attempt 1 with code "timeout" marked retryable, without reporting usage
    And the scripted model streams "Hello" on attempt 2, using 200 input tokens and 50 output tokens
    When a caller invokes "sim-chat"
    Then the metering recorded attempt 1 at 0 input tokens and 0 milli-points
    And the metering recorded attempt 2 at 200 input tokens and 50 milli-points

  @REQ-MODEL-062 @security @id:MODEL-062-01
  Scenario: The call log has an entry per attempt
    Given a scripted chat model that fails attempt 1 with code "vendor_error" marked retryable
    And the scripted model streams "Hi" on attempt 2
    When a caller invokes "sim-chat"
    Then the call log holds 2 entries for attempt 1 and attempt 2 of the call
    And each entry names model "sim-chat", its rung, its start time and its outcome

  @REQ-MODEL-063 @security @id:MODEL-063-01
  Scenario: The call log keeps no secret and no content
    Given chat model "sim-chat" uses the credential "cred://openai-main" whose value is "sk-live-42"
    And a scripted chat model that streams "my secret note" on every attempt
    When a caller invokes "sim-chat" with the run input "transfer 100 to bob"
    Then the call log holds 1 entry
    And no entry contains "sk-live-42", "my secret note" or "transfer 100 to bob"
    And the entry names the credential by its reference "cred://openai-main"

  @REQ-MODEL-064 @id:MODEL-064-01
  Scenario: Logging and recording stay outside a retried attempt
    Given a scripted chat model that fails attempt 1 with code "vendor_error" marked retryable
    And the scripted model streams "Hi" on attempt 2
    When a caller invokes "sim-chat"
    Then the call log holds 2 entries for one call
    And the recording holds 2 attempts of one invocation
