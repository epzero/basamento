@wp:WP-L2-01 @level:component
Feature: Compiling expressions against declared variables
  An expression is compiled once, against the JSON Schemas of the variables
  its expression environment declares, so that a flow, a price table or an
  authorization rule with a wrong expression is rejected before it runs.

  Background:
    Given an expression environment declaring "state" with schema:
      """json
      {
        "type": "object",
        "required": ["failed", "notes"],
        "properties": {
          "failed":   { "type": "integer" },
          "notes":    { "type": "array", "items": { "type": "string" } },
          "reviewer": { "type": "string" }
        }
      }
      """

  @REQ-EXPR-001 @id:EXPR-001-01
  Scenario: An expression that type-checks compiles to its result type
    When the expression "state.failed == 0 && size(state.notes) > 0" is compiled
    Then compilation succeeds with result type "bool"

  @REQ-EXPR-001 @id:EXPR-001-02
  Scenario: Comparing an integer property with a string is rejected
    When the expression "state.failed > 'none'" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the expression and the line and column of the error

  @REQ-EXPR-001 @id:EXPR-001-03
  Scenario: A variable the environment does not declare is rejected
    When the expression "status.failed == 0" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the undeclared reference "status"

  @REQ-EXPR-001 @id:EXPR-001-04
  Scenario: A property the schema does not declare is rejected
    When the expression "state.passed == 0" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the undeclared property "passed"

  @REQ-EXPR-002 @id:EXPR-002-01
  Scenario: A result that does not match the expected schema is rejected
    When the expression "state.failed" is compiled expecting a result of schema {"type": "boolean"}
    Then compilation fails with code "invalid_argument"
    And the error names the expected type "bool" and the actual type "int"

  @REQ-EXPR-002 @id:EXPR-002-02
  Scenario: An integer result is accepted where a number is expected
    When the expression "state.failed" is compiled expecting a result of schema {"type": "number"}
    Then compilation succeeds with result type "int"

  @REQ-EXPR-004 @id:EXPR-004-01
  Scenario: An expression that does not parse is rejected
    When the expression "state.failed >" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the expression and the line and column of the error

  @REQ-EXPR-005 @id:EXPR-005-01
  Scenario Outline: Functions that read the clock, randomness or the environment do not exist
    When the expression "<expression>" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the undeclared reference "<function>"

    Examples:
      | expression                                | function |
      | now() > timestamp('2026-01-01T00:00:00Z') | now      |
      | rand() < 0.5                              | rand     |
      | random() < 0.5                            | random   |
      | uuid() != ''                              | uuid     |
      | env('HOME') != ''                         | env      |
      | readFile('/etc/hosts') != ''              | readFile |
