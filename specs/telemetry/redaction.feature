@planned @wp:WP-L1-07 @level:component
Feature: Secrets and payload data never reach a log record, span attribute or metric label
  Whatever is recorded, a credential value, run payload data or a field named
  after a secret never leaves the process. Redaction runs before anything is
  kept or exported.

  Background:
    Given an embedded telemetry setup with a fake clock
    And the telemetry setup holds a credential "openai" with the value "sk-live-7f3a9c1b"

  @REQ-TELEM-006 @security @id:TELEM-006-01
  Scenario: A credential value is removed from every signal
    When a log record, a span attribute and a metric label each carry the text "key sk-live-7f3a9c1b in use"
    Then no kept log record, span attribute or metric label contains "sk-live-7f3a9c1b"
    And each records "[REDACTED]"

  @REQ-TELEM-006 @security @id:TELEM-006-02
  Scenario Outline: Payload data is removed from every signal
    When a log record, a span attribute and a metric label each carry "<text>"
    Then no kept log record, span attribute or metric label contains "<payload>"
    And each records "[REDACTED]"

    Examples:
      | text                          | payload                     |
      | prompt Write a haiku          | Write a haiku               |
      | output the haiku is done      | the haiku is done           |
      | input {"order": 42}           | {"order": 42}               |
      | history turn 3 said hello     | turn 3 said hello           |

  @REQ-TELEM-006 @security @id:TELEM-006-03
  Scenario Outline: A field named after a secret loses its value at any depth
    When a log record carries "password" with the value "hunter2" under the key path "<path>"
    Then the log record records "[REDACTED]" at "<path>"

    Examples:
      | path           |
      | password       |
      | secret         |
      | token          |
      | authorization  |
      | user.password  |
      | request.token  |

  @REQ-TELEM-006 @security @id:TELEM-006-04
  Scenario: A field only containing a secret word keeps its value
    When a log record carries 12
    And the log record names the field "token_count"
    Then the log record records 12 at "token_count"

  @REQ-TELEM-006 @security @id:TELEM-006-05
  Scenario: Run payload data is removed from a span
    Given the payload of run "run_01" is "order 42 shipped"
    When span "step a" records attribute "output" with the value "order 42 shipped"
    Then span "step a" records "[REDACTED]" at attribute "output"

  @REQ-TELEM-007 @security @id:TELEM-007-01
  Scenario: A redacted value is redacted before it is kept
    When a span "step a" records attribute "note" with the value "sk-live-7f3a9c1b"
    Then the span store row of span "step a" attributes contains "note=[REDACTED]"

  @REQ-TELEM-007 @security @id:TELEM-007-02
  Scenario: A redacted value is redacted before it is exported
    Given a cloud telemetry setup exporting to a simulated collector
    When a log record carrying "sk-live-7f3a9c1b" is written
    And the telemetry setup is shut down
    Then every export batch the collector received records "[REDACTED]"

  @REQ-TELEM-008 @security @id:TELEM-008-01
  Scenario: A secret hiding inside a long value is cut, not trimmed
    When a log record carries "x" repeated 300 times, "sk-live-7f3a9c1b", and "y" repeated 300 times
    Then the log record records "[REDACTED]"

  @REQ-TELEM-008 @security @id:TELEM-008-02
  Scenario: A payload inside a long value is cut, not trimmed
    When a log record carries "x" repeated 600 times and the payload "SECRET PLAN" of run "run_01"
    Then the log record records "[REDACTED]"

  @REQ-TELEM-008 @security @id:TELEM-008-03
  Scenario: A long value without a secret is kept whole
    When a log record carries "x" repeated 700 times
    Then the log record records 700 characters at "message"

  @REQ-TELEM-009 @security @id:TELEM-009-01
  Scenario: The redactor leaves other values unchanged
    When the field "note" carries the value "step a finished in 250ms" to the redactor
    Then the redactor returns "step a finished in 250ms"
