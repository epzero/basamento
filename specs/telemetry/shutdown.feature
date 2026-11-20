@wp:WP-L1-07 @level:component
Feature: Shutdown flushes what was recorded and stops telemetry
  A process that shuts down sends what it already recorded before it exits,
  and nothing is recorded after it.

  @REQ-TELEM-022 @profile:cloud @id:TELEM-022-01
  Scenario: Shutdown delivers an unexported span before it returns
    Given a cloud telemetry setup with a fake clock that exports to a simulated collector
    And run "run_01" of tenant "acme" is running
    And span "step a" is started and ended
    When the telemetry setup shuts down
    Then the collector received 1 span named "step a"
    And the telemetry setup returned only after the collector confirmed it

  @REQ-TELEM-022 @profile:cloud @id:TELEM-022-02
  Scenario: Shutdown delivers the log records and metrics already recorded
    Given a cloud telemetry setup with a fake clock that exports to a simulated collector
    And a log record "started" is written and metric "runs.started" is recorded with the value 1
    When the telemetry setup shuts down
    Then the collector received 1 log record with message "started" and 1 metric point named "runs.started"

  @REQ-TELEM-023 @profile:embedded @id:TELEM-023-01
  Scenario: Nothing is recorded after shutdown
    Given an embedded telemetry setup with a fake clock
    And the telemetry setup has shut down
    And run "run_01" of tenant "acme" is running
    When span "late" is started and ended, a log record "late" is written, and metric "runs.started" is recorded with the value 1
    Then the span store holds 0 spans
    And the telemetry setup holds 0 log records and 0 metric points
