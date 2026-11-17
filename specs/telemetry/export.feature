@planned @wp:WP-L1-07 @level:component @profile:cloud
Feature: The cloud profile exports over OTLP and never lets export break the work
  In the cloud profile, spans, metrics and log records go to the collector over
  OTLP. Telemetry observes the work; it never makes it wait, fail or change its
  result. What cannot be delivered is dropped.

  Background:
    Given a cloud telemetry setup with a fake clock that exports to a simulated collector

  @REQ-TELEM-015 @id:TELEM-015-01
  Scenario: Ended spans reach the collector over OTLP
    Given run "run_01" of tenant "acme" is running
    When span "step a" is started and ended
    And the telemetry setup is flushed
    Then the collector received 1 span named "step a" over OTLP

  @REQ-TELEM-015 @id:TELEM-015-02
  Scenario: Recorded metrics and log records reach the collector over OTLP
    When metric "runs.started" is recorded with the value 1 and a log record "started" is written
    And the telemetry setup is flushed
    Then the collector received 1 metric point named "runs.started" with the value 1 over OTLP
    And the collector received 1 log record with message "started" over OTLP

  @REQ-TELEM-016 @id:TELEM-016-01
  Scenario: A slow collector makes no signal wait
    Given the simulated collector answers after 5 seconds of clock time
    When span "step a" is started and ended, a log record "started" is written, and metric "runs.started" is recorded with the value 1
    Then recording returns without the clock advancing
    And the collector has received 0 spans

  @REQ-TELEM-016 @id:TELEM-016-02
  Scenario: An unreachable collector makes no signal wait
    Given the simulated collector is unreachable
    When span "step a" is started and ended, a log record "started" is written, and metric "runs.started" is recorded with the value 1
    Then recording returns without the clock advancing

  @REQ-TELEM-017 @id:TELEM-017-01
  Scenario: An undelivered batch is dropped rather than retried forever
    Given the simulated collector is unreachable
    And run "run_01" of tenant "acme" is running
    When span "step a" is started and ended, a log record "started" is written, and metric "runs.started" is recorded with the value 1
    And the telemetry setup makes 1 export attempt
    And the simulated collector becomes reachable
    And the telemetry setup is flushed
    Then the collector received 0 spans, 0 log records and 0 metric points

  @REQ-TELEM-019 @id:TELEM-019-01
  Scenario: A span completes without error when export fails
    Given the simulated collector is unreachable
    When span "step a" is started and ended with status "ok"
    And the telemetry setup makes 1 export attempt
    Then span "step a" ends with status "ok"

  @REQ-TELEM-019 @id:TELEM-019-02
  Scenario: Recording a log record and a metric completes without error when export fails
    Given the simulated collector is unreachable
    When a log record "started" is written and metric "runs.started" is recorded with the value 1
    And the telemetry setup makes 1 export attempt
    Then recording the log record and the metric point returns no error
