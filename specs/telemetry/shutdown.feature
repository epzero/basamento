@planned @wp:WP-L1-07 @level:component
Feature: Shutdown flushes what was recorded and stops telemetry
  A process that shuts down sends what it already recorded before it exits,
  and nothing is recorded after it.

  Background:
    Given an embedded telemetry setup with a fake clock

  @REQ-TELEM-022 @id:TELEM-022-01
  Scenario: Shutdown delivers an unexported span before it returns
    Given a simulated collector that is unreachable
    And run "run_01" of tenant "acme" is running
    And span "step a" is started and ended
    When the simulated collector becomes reachable and the telemetry setup shuts down
    Then the collector received 1 span named "step a"
    And the telemetry setup returned only after the collector confirmed it

  @REQ-TELEM-022 @id:TELEM-022-02
  Scenario: Shutdown delivers the log records and metrics already recorded
    Given a simulated collector that is unreachable
    And a log record and a metric are recorded
    When the simulated collector becomes reachable and the telemetry setup shuts down
    Then the collector received 1 log record and 1 metric point

  @REQ-TELEM-023 @id:TELEM-023-01
  Scenario: A span started after shutdown is not recorded
    Given the telemetry setup has shut down
    And run "run_01" of tenant "acme" is running
    When span "late" is started and ended
    Then the span store holds 0 spans
