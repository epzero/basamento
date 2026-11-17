@planned @wp:WP-L3-06 @level:component
Feature: Model catalog and price table
  The catalog holds a descriptor for every model id and the price table holds
  what a call of it costs: rates shipped with the binary, overridden per
  deployment and per tenant, and the formula that turns rates and usage into
  whole milli-points.

  Background:
    Given the catalog holds a "chat" model "sim-chat"
    And the model "sim-chat" is priced by the shipped snapshot at "usage.input_tokens * 1 + params.max_tokens * 8" milli-points
    And the clock is frozen at "2026-10-01T12:00:00Z"

  @REQ-MODEL-066 @id:MODEL-066-01
  Scenario: A model id finds its descriptor
    Given the descriptor of "sim-chat" declares the input slots "messages" and "prompt"
    And the descriptor of "sim-chat" declares the vendor protocol "stream" and the locality "cloud"
    When the catalog looks up "sim-chat"
    Then the catalog returns a descriptor whose capability is "chat", with input slots "messages" and "prompt"
    And the descriptor declares the vendor protocol "stream" and the locality "cloud"

  @REQ-MODEL-067 @id:MODEL-067-01
  Scenario: An unknown model id is not found
    When the catalog looks up "no-such-model"
    Then the lookup fails with code "not_found"

  @REQ-MODEL-068 @cost @id:MODEL-068-01
  Scenario: A rate rule applies when its condition holds
    Given the model "sim-chat" has the rate rules:
      | when                           | input | output |
      | at.getHours('UTC') >= 17       | 0.14  | 0.55   |
      | usage.input_tokens > 200000    | 0.56  | 2.20   |
      | always                         | 0.28  | 1.10   |
    When the clock reads "2026-10-01T18:00:00Z" and a call spends 1000 input tokens and 500 output tokens
    Then 465 milli-points are charged for the call

  @REQ-MODEL-069 @cost @id:MODEL-069-01
  Scenario: The first rule that holds applies
    Given the model "sim-chat" has the rate rules:
      | when                           | input | output |
      | at.getHours('UTC') >= 17       | 0.14  | 0.55   |
      | usage.input_tokens > 200000    | 0.56  | 2.20   |
      | always                         | 0.28  | 1.10   |
    When the clock reads "2026-10-01T20:00:00Z" and a call spends 200100 input tokens and 1000 output tokens
    Then the call is priced at the rule "at.getHours('UTC') >= 17" and costs 575 milli-points

  @REQ-MODEL-070 @cost @id:MODEL-070-01
  Scenario: A tenant override beats the deployment override and the snapshot
    Given the deployment overrides the price of "sim-chat" at "usage.input_tokens * 2 + params.max_tokens * 8" milli-points
    And tenant "acme" overrides the price of "sim-chat" at "usage.input_tokens * 3 + params.max_tokens * 8" milli-points
    When tenant "acme" makes a call of "sim-chat" with 1000 input tokens and an output limit of 500 tokens
    Then 7000 milli-points are estimated for the call
    And the reservation of the call is 7000 milli-points

  @REQ-MODEL-070 @cost @id:MODEL-070-02
  Scenario: Without a tenant override the deployment override applies
    Given the deployment overrides the price of "sim-chat" at "usage.input_tokens * 2 + params.max_tokens * 8" milli-points
    And tenant "acme" has no override for the price of "sim-chat"
    When tenant "acme" makes a call of "sim-chat" with 1000 input tokens and an output limit of 500 tokens
    Then 6000 milli-points are estimated for the call
    And the reservation of the call is 6000 milli-points

  @REQ-MODEL-070 @cost @id:MODEL-070-03
  Scenario: Without any override the shipped snapshot applies
    When tenant "acme" makes a call of "sim-chat" with 1000 input tokens and an output limit of 500 tokens
    Then 5000 milli-points are estimated for the call
    And the reservation of the call is 5000 milli-points

  @REQ-MODEL-071 @security @cost @id:MODEL-071-01
  Scenario: One tenant's override never prices another tenant's call
    Given tenant "acme" overrides the price of "sim-chat" at "usage.input_tokens * 3 + params.max_tokens * 8" milli-points
    When tenant "globex" makes a call of "sim-chat" with 1000 input tokens and an output limit of 500 tokens
    Then 5000 milli-points are estimated for the call

  @REQ-MODEL-071 @security @cost @id:MODEL-071-02
  Scenario: A call without a tenant never takes a tenant's override
    Given tenant "acme" overrides the price of "sim-chat" at "usage.input_tokens * 3 + params.max_tokens * 8" milli-points
    When a call of "sim-chat" outside any tenant spends 1000 input tokens and 500 output tokens
    Then 5000 milli-points are charged for the call

  @REQ-MODEL-072 @cost @id:MODEL-072-01
  Scenario: A model without a price is not called
    Given the catalog holds a "chat" model "sim-unpriced" whose rates hold only when "usage.input_tokens > 1000000"
    When a run invokes "sim-unpriced" with 1000 input tokens
    Then the node fails with code "unsupported"
    And the provider accepted 0 invocations

  @REQ-MODEL-073 @cost @id:MODEL-073-01
  Scenario: The default token formula uses the chosen per-million rates
    Given the model "sim-chat" has the rate rules "input 0.25 and output 10" per million tokens
    When a call of "sim-chat" spends 2000 input tokens and 300 output tokens
    Then 3 milli-points are charged for the call

  @REQ-MODEL-074 @cost @id:MODEL-074-01
  Scenario: Cached and reasoning tokens are not counted by the default formula
    Given the model "sim-chat" has the rate rules "input 0.25 and output 10" per million tokens
    When a call of "sim-chat" spends 2000 input tokens, of which 500 cached, and 300 output tokens, of which 100 reasoning
    Then 3 milli-points are charged for the call

  @REQ-MODEL-075 @cost @id:MODEL-075-01
  Scenario: An exact half rounds up to a whole milli-point
    Given the model "sim-chat" has the rate rules "input 0.25 and output 0" per million tokens
    When a call of "sim-chat" spends 2000 input tokens and 0 output tokens
    Then 1 milli-point is charged for the call

  @REQ-MODEL-075 @cost @id:MODEL-075-02
  Scenario: An amount under a half rounds down to the whole milli-point
    Given the model "sim-chat" has the rate rules "input 0.25 and output 0" per million tokens
    When a call of "sim-chat" spends 1999 input tokens and 0 output tokens
    Then 0 milli-points are charged for the call

  @REQ-MODEL-076 @cost @id:MODEL-076-01
  Scenario: The estimate counts input tokens and the output limit
    Given a scripted chat model that would use 4000 input tokens and 100 output tokens
    When a run invokes "sim-chat" with an output limit of 500 tokens
    Then 5000 milli-points are estimated for the call
    And the reservation of the call is 5000 milli-points

  @REQ-MODEL-077 @cost @id:MODEL-077-01
  Scenario: A later price change does not reprice a settled call
    Given a run invoked "sim-chat" with an output limit of 500 tokens and the call is still running
    When the shipped snapshot then prices "sim-chat" at "usage.input_tokens * 9 + params.max_tokens * 8" milli-points
    And the provider reports success with usage 1000 input tokens and 500 output tokens
    Then 5000 milli-points are estimated for the call
    And 5000 milli-points were charged for the call
