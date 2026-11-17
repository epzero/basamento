@planned @wp:WP-L3-02 @level:component
Feature: Structured output climbs the ladder on one attempt budget
  A chat call that must answer with JSON walks four rungs, from the most
  reliable to the last resort, and stops when its attempt budget is spent.

  Background:
    Given a chat model "sim-chat" whose output schema requires an integer field "answer"

  @REQ-MODEL-043 @cost @id:MODEL-043-01
  Scenario: Without a setting the budget gives three attempts
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call made 3 attempts

  @REQ-MODEL-043 @cost @id:MODEL-043-02
  Scenario: A call may set a smaller budget
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    And a call allows 2 attempts
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call made 2 attempts

  @REQ-MODEL-044 @cost @id:MODEL-044-01
  Scenario: The first attempt uses the first rung the model supports
    Given chat model "sim-chat" supports the rungs "JSON mode, forced tool call"
    And the scripted model returns satisfying output on every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then attempt 1 used the rung "JSON mode"

  @REQ-MODEL-044 @cost @id:MODEL-044-02
  Scenario: An attempt that misses the schema moves to the next rung the model supports
    Given chat model "sim-chat" supports the rungs "native schema, forced tool call"
    And the scripted model returns a JSON answer that misses the schema on attempt 1 and then satisfying output
    When a call asks "sim-chat" for output that satisfies the output schema
    Then attempt 1 used the rung "native schema"
    And attempt 2 used the rung "forced tool call"

  @REQ-MODEL-044 @cost @id:MODEL-044-03
  Scenario: A retryable failure is retried on the same rung before the ladder moves on
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model fails attempt 1 with code "unavailable" marked retryable
    And the scripted model returns a JSON answer that misses the schema on attempt 2 and then satisfying output
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a call asks "sim-chat" for output that satisfies the output schema
    Then attempt 1 used the rung "native schema"
    And attempt 2 used the rung "native schema"
    And attempt 3 used the rung "JSON mode"

  @REQ-MODEL-045 @cost @id:MODEL-045-01
  Scenario: Each model invocation uses at most one rung of the ladder
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then invocation 1 used only the rung "native schema"
    And invocation 2 used only the rung "JSON mode"

  @REQ-MODEL-046 @id:MODEL-046-01
  Scenario: A model that supports no rung is not called
    Given chat model "sim-chat" supports the rungs ""
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call failed with code "unsupported"
    And the provider accepted 0 invocations

  @REQ-MODEL-047 @cost @id:MODEL-047-01
  Scenario: Output that still misses the schema after the last attempt fails the call
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode, forced tool call"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call failed with code "invalid_argument"
    And the call's error says no attempt satisfied the output schema

  @REQ-MODEL-047 @cost @id:MODEL-047-02
  Scenario: A spent ladder fails the call at the repair even with budget left
    Given chat model "sim-chat" supports the rungs "JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    And a call allows 5 attempts
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call failed with code "invalid_argument"
    And the call made 2 attempts
    And the provider accepted 1 invocation

  @REQ-MODEL-054 @cost @id:MODEL-054-01
  Scenario: Ladder attempts carry distinct deterministic client refs derived from the call's
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    When a call with client ref "call-1" asks "sim-chat" for output that satisfies the output schema
    And another call with client ref "call-1" asks "sim-chat" for output that satisfies the output schema
    Then each call caused the provider to accept 2 invocations
    And for both calls invocation 1 carried the client ref "call-1"
    And both calls' attempt 2 invocations carried the same client ref derived from "call-1" and distinct from "call-1"

  @REQ-MODEL-054 @cost @id:MODEL-054-02
  Scenario: Retrying an unacknowledged start reuses that attempt's client ref
    Given chat model "sim-chat" supports the rungs "native schema"
    And the scripted model fails start 1 without acknowledging acceptance with code "unavailable" marked retryable and returns satisfying output on the next start
    And the call's seeded randomness draws 0 milliseconds of jitter
    When a call with client ref "call-1" asks "sim-chat" for output that satisfies the output schema
    Then the provider received 2 start requests both carrying the client ref "call-1"

  @REQ-MODEL-065 @cost @id:MODEL-065-01
  Scenario: The attempt after the last supported rung repairs the JSON locally
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then attempts 1 and 2 used the rungs "native schema" and "JSON mode"
    And attempt 3 repaired the JSON locally without a model invocation
    And the provider accepted 2 invocations

  @REQ-MODEL-065 @cost @id:MODEL-065-02
  Scenario: Trailing commas in the answer are repaired without a model invocation
    Given chat model "sim-chat" supports the rungs "JSON mode"
    And a call allows 2 attempts
    And the scripted model returns the answer
      """
      ```json
      {"answer": 7,}
      ```
      Hope this helps!
      """
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call made 2 attempts
    And attempt 1 used the rung "JSON mode"
    And attempt 2 repaired the JSON locally without a model invocation
    And the provider accepted 1 invocation
    And the call returns the JSON {"answer": 7}
