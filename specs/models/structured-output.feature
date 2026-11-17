@planned @wp:WP-L3-02 @level:component
Feature: Structured output climbs the ladder on one attempt budget
  A chat call that must answer with JSON walks four rungs, from the most
  reliable to the last resort, and stops when its attempt budget is spent.

  Background:
    Given a chat model "sim-chat" whose output schema requires an integer field "answer"

  @REQ-MODEL-043 @cost @id:MODEL-043-01
  Scenario: Without a setting the budget gives three attempts
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model fails every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call made 3 attempts

  @REQ-MODEL-043 @cost @id:MODEL-043-02
  Scenario: A call may set a smaller budget
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model fails every attempt
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

  @REQ-MODEL-045 @cost @id:MODEL-045-01
  Scenario: The last attempt that has no further rung repairs the JSON locally
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then attempts 1 and 2 used the rungs "native schema" and "JSON mode"
    And attempt 2 was repaired locally without a model call
    And the call made 3 invocations

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
  Scenario: A repair that fixes the JSON uses the call's last attempt
    Given chat model "sim-chat" supports the rungs "JSON mode"
    And the scripted model returns a JSON answer that misses the schema on every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the call failed with code "invalid_argument"
    And the call made 3 attempts

  @REQ-MODEL-045 @cost @id:MODEL-045-02
  Scenario: A repaired answer is the call's result
    Given chat model "sim-chat" supports the rungs "JSON mode"
    And the scripted model answers with usage of 100 input tokens and 20 output tokens
    And the repair of that answer satisfies the output schema
    When a call asks "sim-chat" for output that satisfies the output schema with 1 attempt at most
    Then the call returns the repaired answer
    And the call fails no attempt for a schema mismatch

  @REQ-MODEL-054 @cost @id:MODEL-054-01
  Scenario: Ladder attempts carry one client ref
    Given chat model "sim-chat" supports the rungs "native schema, JSON mode"
    And the scripted model fails every attempt
    When a call asks "sim-chat" for output that satisfies the output schema
    Then the provider accepted 2 invocations
    And both invocations carried the same client ref
