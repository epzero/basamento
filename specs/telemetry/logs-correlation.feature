@planned @wp:WP-L1-07 @level:component
Feature: Log records carry the trace, run and tenant they belong to
  One setup emits log records, spans and metrics. Every log record can be
  joined to the span it came from, the run it belongs to and the tenant that
  ran it.

  Background:
    Given an embedded telemetry setup with a fake clock

  @REQ-TELEM-001 @id:TELEM-001-01
  Scenario: One setup emits logs, spans and metrics
    Given tenant "acme" runs run "run_01"
    When a span "step a" is started and ended with a log record and a metric
    Then the telemetry setup holds 1 span "step a" for run "run_01"
    And it holds a log record and a metric

  @REQ-TELEM-002 @id:TELEM-002-01
  Scenario: A second setup in one process is refused
    When another telemetry setup is created in the same process
    Then the telemetry port fails with code "conflict"

  @REQ-TELEM-003 @id:TELEM-003-01
  Scenario: Times come from the setup's clock
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    When a span "step a" is started and ended
    Then span "step a" starts at "2026-10-01T09:00:00Z" and ends at "2026-10-01T09:00:00Z"

  @REQ-TELEM-003 @id:TELEM-003-02
  Scenario: Ending a span takes no wall clock time
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a span "step a" is started
    When 250 milliseconds pass on the clock
    And span "step a" is ended
    Then span "step a" lasts 250 milliseconds

  @REQ-TELEM-004 @id:TELEM-004-01
  Scenario: Every log record has its own event id
    When 100 log records are written
    Then each of the 100 log records has an "event_id" attribute
    And the 100 "event_id" values are distinct

  @REQ-TELEM-005 @id:TELEM-005-01
  Scenario: A log record written in a span names its span, run and tenant
    Given tenant "acme" runs run "run_01"
    When a log record is written inside a span "step a" of run "run_01"
    Then the log record has the trace id and span id of span "step a"
    And the log record has attribute "run_id" with the value of run "run_01"
    And the log record has attribute "tenant" with the value "acme"

  @REQ-TELEM-005 @id:TELEM-005-02
  Scenario: A log record written outside any span names no span
    Given tenant "acme" runs run "run_01"
    When a log record is written outside any span
    Then the log record has attribute "trace_id" with the empty value
    And the log record has attribute "span_id" with the empty value
