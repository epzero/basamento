@wp:WP-L3-02 @level:component
Feature: Middleware around every chat call
  A chat call goes through the call log, the recording, the fallback, the
  metering, the retry middleware and the timeouts, in that order: logging and
  recording stay outer, so a retry costs two log entries but is one
  logical call.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"

  @REQ-MODEL-048 @cost @id:MODEL-048-01
  Scenario: A provider error that may pass is tried again
    Given a scripted chat model that fails attempt 1 with code "vendor_error" marked retryable and streams "Hello" on later attempts
    When a caller invokes "sim-chat"
    Then the call succeeded with message text "Hello"
    And the call made 2 attempts

  @REQ-MODEL-066 @cost @id:MODEL-066-01
  Scenario: A retryable error with the attempt budget spent fails the call
    Given a scripted chat model that fails attempt 1 with code "unavailable" marked retryable and fails every later attempt the same way
    And a call allows 2 attempts
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller invokes "sim-chat"
    Then the call failed with code "unavailable"
    And the call made 2 attempts
    And the provider accepted 2 invocations
    And invocation 2 started 1000 milliseconds after invocation 1 ended

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

  @REQ-MODEL-051 @cost @id:MODEL-051-01
  Scenario: Waits double from one second, with seeded jitter
    Given a scripted chat model that fails its first 2 attempts with code "unavailable" marked retryable and no Retry-After
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller invokes "sim-chat"
    Then invocation 2 started 1000 milliseconds after invocation 1 ended
    And invocation 3 started 2000 milliseconds after invocation 2 ended

  @REQ-MODEL-051 @cost @id:MODEL-051-02
  Scenario: Jitter is added to the base wait without altering the next base wait
    Given a scripted chat model that fails its first 2 attempts with code "unavailable" marked retryable and no Retry-After
    And the call's seeded randomness draws 500 milliseconds of jitter for retry 1 and 0 milliseconds for retry 2
    When a caller invokes "sim-chat"
    Then invocation 2 started 1500 milliseconds after invocation 1 ended
    And invocation 3 started 2000 milliseconds after invocation 2 ended

  @REQ-MODEL-052 @cost @id:MODEL-052-01
  Scenario: A Retry-After longer than the computed wait is honoured
    Given a scripted chat model that fails attempt 1 with code "rate_limited" marked retryable and a Retry-After of 5 seconds
    When a caller invokes "sim-chat"
    Then invocation 2 started 5000 milliseconds after invocation 1 ended

  @REQ-MODEL-052 @cost @id:MODEL-052-02
  Scenario: A Retry-After shorter than the computed wait is not used
    Given a scripted chat model that fails attempt 1 with code "rate_limited" marked retryable and a Retry-After of 500 milliseconds
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller invokes "sim-chat"
    Then invocation 2 started 1000 milliseconds after invocation 1 ended

  @REQ-MODEL-053 @cost @id:MODEL-053-01
  Scenario: A wait that reaches the call total timeout fails the call
    Given a scripted chat model that fails attempt 1 with code "unavailable" marked retryable and a Retry-After of 5 seconds
    When a caller starts an invocation with a call total timeout of 3 seconds
    Then the call failed with code "timeout"
    And the call made 1 attempt

  @REQ-MODEL-053 @cost @id:MODEL-053-02
  Scenario: A call total timeout ends the ladder of structured output
    Given chat model "sim-chat" whose output schema requires an integer field "answer"
    And chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And a scripted chat model that fails attempt 1 with code "timeout" marked retryable and a Retry-After of 4 seconds
    When a caller starts a call that asks "sim-chat" for output that satisfies the output schema with a call total timeout of 3 seconds
    Then the call failed with code "timeout"
    And the call made 1 attempt

  @REQ-MODEL-055 @id:MODEL-055-01
  Scenario: A new attempt starts with a reset naming it
    Given a scripted chat model that fails attempt 1 with code "unavailable" marked retryable and streams "Hi" in 2 deltas on later attempts
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller starts an invocation and follows its events
    And 1 second passes
    Then the caller receives a "reset" event for attempt 2 with attempt number 2
    And the caller receives exactly 2 text events, all of attempt 2
    And the result message text is "Hi"

  @REQ-MODEL-056 @cost @id:MODEL-056-01
  Scenario: An attempt that never starts streaming times out at the first-chunk timeout and is retried
    Given a scripted chat model that never produces a first event on attempt 1 and streams "Hi" on attempt 2
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller starts an invocation with a first-chunk timeout of 10 seconds and follows its events
    And 11 seconds pass
    Then attempt 1 was cancelled with code "timeout"
    And the call made 2 attempts

  @REQ-MODEL-057 @id:MODEL-057-01
  Scenario: An attempt that falls quiet times out at the idle timeout
    Given a scripted chat model that streams 1 delta at 0 seconds and then falls quiet
    When a caller starts an invocation with an idle timeout of 30 seconds and follows its events
    And 30 seconds pass
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-058 @id:MODEL-058-01
  Scenario: A stream that keeps talking is stopped at the total timeout
    Given a scripted chat model that streams 1 delta every 5 seconds starting at 0 seconds
    When a caller starts an invocation with an attempt total timeout of 28 seconds and follows its events
    And 28 seconds pass
    Then attempt 1 was cancelled with code "timeout"
    And the caller received 6 text events

  @REQ-MODEL-059 @id:MODEL-059-01
  Scenario: The defaults time out a silent stream at 30 seconds
    Given a scripted chat model that never produces a first event
    When a caller starts an invocation and follows its events
    And 30 seconds pass
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-059 @id:MODEL-059-02
  Scenario: The defaults time out a quiet stream at 60 seconds
    Given a scripted chat model that streams 1 delta at 0 seconds and then falls quiet
    When a caller starts an invocation and follows its events
    And 59 seconds pass
    Then attempt 1 was not cancelled
    When 1 more second passes
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-059 @id:MODEL-059-03
  Scenario: The defaults stop a long stream at 10 minutes
    Given a scripted chat model that streams 1 delta every 5 seconds
    When a caller starts an invocation and follows its events
    And 10 minutes pass
    Then attempt 1 was cancelled with code "timeout"

  @REQ-MODEL-060 @cost @id:MODEL-060-01
  Scenario: Every attempt that reached the provider is metered
    Given a model "sim-chat" priced at "(usage.input_tokens * 1 + usage.output_tokens * 8) / 1000" milli-points
    And a scripted chat model that fails attempt 1 with code "vendor_error" marked retryable, using 1000 input tokens and 0 output tokens
    And the scripted model streams "Hello" on attempt 2, using 2000 input tokens and 500 output tokens
    When a caller invokes "sim-chat"
    Then the metering recorded 3000 input tokens and 500 output tokens
    And the metering recorded attempt 1 at 1 milli-point and attempt 2 at 6 milli-points

  @REQ-MODEL-061 @cost @id:MODEL-061-01
  Scenario: An attempt the provider does not bill costs nothing
    Given a model "sim-chat" priced at "(usage.input_tokens * 1 + usage.output_tokens * 8) / 1000" milli-points
    And a scripted chat model that fails attempt 1 with code "timeout" marked retryable, without reporting usage
    And the scripted model streams "Hello" on attempt 2, using 2000 input tokens and 500 output tokens
    When a caller invokes "sim-chat"
    Then the metering recorded attempt 1 at 0 input tokens and 0 milli-points
    And the metering recorded attempt 2 at 2000 input tokens and 6 milli-points

  @REQ-MODEL-062 @id:MODEL-062-01
  Scenario: The call log has an entry per attempt
    Given a scripted chat model that fails attempt 1 with code "vendor_error" marked retryable
    And the scripted model streams "Hi" on attempt 2
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller invokes "sim-chat" with client ref "call-log-1"
    Then the call log holds 2 entries for attempt 1 and attempt 2 of the call
    And entry 1 names the client ref "call-log-1" and entry 2 names the distinct deterministic client ref derived from "call-log-1" for attempt 2
    And each entry names model "sim-chat", the rung "none", its start time and its outcome

  @REQ-MODEL-063 @security @id:MODEL-063-01
  Scenario: The call log keeps no secret and no content
    Given chat model "sim-chat" uses the credential "cred://openai-main" whose value is "sk-live-42"
    And a scripted chat model that streams "my secret note" on every attempt
    When a caller invokes "sim-chat" with the message "transfer 100 to bob"
    Then the call log holds 1 entry
    And no entry contains "sk-live-42", "my secret note" or "transfer 100 to bob"
    And the entry names the credential by its reference "cred://openai-main"

  @REQ-MODEL-064 @id:MODEL-064-01
  Scenario: The middleware chain runs in order from call log down to timeouts
    Given a scripted chat model that never produces a first event on attempt 1 and streams "Hi" on attempt 2
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller starts an invocation with a first-chunk timeout of 10 seconds and follows its events
    And 11 seconds pass
    Then attempt 1 was cancelled with code "timeout" before the retry middleware started attempt 2
    And the metering recorded 2 attempts while the fallback middleware was not triggered
    And the call log holds 2 entries and the recording holds 1 call with 2 attempts

  @REQ-MODEL-064 @cost @id:MODEL-064-02
  Scenario: Recording wraps the inner middlewares inside the call log
    Given a scripted chat model that fails attempt 1 with code "unavailable" marked retryable and fails every later attempt the same way
    And a call allows 2 attempts
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a caller invokes "sim-chat" and records the call
    And a caller invokes "sim-chat" replaying that recording
    Then the first call failed with code "unavailable" after 2 attempts
    And the replay failed with code "unavailable"
    And the provider accepted 2 invocations in total across both calls
    And the metering recorded 2 attempts in total across both calls
    And the call log holds 3 entries across both calls
