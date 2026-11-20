@wp:WP-L1-07 @level:component
Feature: Secrets never reach a log record, span attribute or metric label
  Whatever is recorded, a credential value or a field named after a secret
  never leaves the process. Redaction runs before anything is kept or exported.

  @REQ-TELEM-006 @security @profile:embedded @id:TELEM-006-01
  Scenario: A credential value is removed from every signal
    Given an embedded telemetry setup with a fake clock
    And the telemetry setup holds a credential "openai" with the value "sk-live-7f3a9c1b"
    When a log record, a span attribute and a metric label each carry the text "key sk-live-7f3a9c1b in use"
    Then no kept log record, span attribute or metric label contains "sk-live-7f3a9c1b"
    And each records "[REDACTED]"

  @REQ-TELEM-006 @security @profile:embedded @id:TELEM-006-03
  Scenario Outline: A field named after a secret loses its value at any depth
    Given an embedded telemetry setup with a fake clock
    When a log record, a span attribute and a metric label carry the value "hunter2" under the key path "<path>"
    Then the log record, the span attribute and the metric label record "[REDACTED]" at "<path>"

    Examples:
      | path           |
      | password       |
      | secret         |
      | token          |
      | authorization  |
      | Authorization  |
      | PASSWORD       |
      | User.Secret    |
      | user.password  |
      | request.token  |

  @REQ-TELEM-006 @security @profile:embedded @id:TELEM-006-04
  Scenario: A field whose name only contains a secret word keeps its value
    Given an embedded telemetry setup with a fake clock
    When a log record, a span attribute and a metric label carry 12 at "token_count" and "step finished" at "note"
    Then the log record, the span attribute and the metric label record 12 at "token_count" and "step finished" at "note"

  @REQ-TELEM-007 @security @profile:embedded @id:TELEM-007-01
  Scenario: A redacted value is redacted before it is kept
    Given an embedded telemetry setup with a fake clock
    And the telemetry setup holds a credential "openai" with the value "sk-live-7f3a9c1b"
    And run "run_01" of tenant "acme" is running
    And span "step a" is started
    When span "step a" records attribute "note" with the value "sk-live-7f3a9c1b"
    And span "step a" is ended
    Then the kept span "step a" has attribute "note" with the value "[REDACTED]"

  @REQ-TELEM-007 @security @profile:cloud @id:TELEM-007-02
  Scenario: A redacted value is redacted before it is exported
    Given a cloud telemetry setup with a fake clock that exports to a simulated collector
    And the telemetry setup holds a credential "openai" with the value "sk-live-7f3a9c1b"
    When a log record carrying "sk-live-7f3a9c1b" is written
    And the telemetry setup is shut down
    Then the collector received 1 log record with message "[REDACTED]" over OTLP
    And no export batch the collector received contains "sk-live-7f3a9c1b"
