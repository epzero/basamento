@planned @wp:WP-L1-07 @level:component @profile:cloud
Feature: The cloud profile exports over OTLP and never lets export break the work
  In the cloud profile, spans and metrics go to the collector over OTLP.
  Telemetry observes the work; it never makes it wait, fail or change its
  result. What cannot be delivered is dropped and counted.

  Background:
    Given a cloud telemetry setup with a fake clock that exports to a simulated collector

  @REQ-TELEM-015 @id:TELEM-015-01
  Scenario: Ended spans reach the collector over OTLP gRPC
    Given run "run_01" of tenant "acme" is running
    When span "step a" is started and ended
    And the telemetry setup is flushed
    Then the collector received every batch over OTLP gRPC
    And the collector received 1 span named "step a"

  @REQ-TELEM-015 @id:TELEM-015-02
  Scenario: Recorded metrics reach the collector over OTLP gRPC
    When metric "runs.started" is recorded with the value 1
    And the telemetry setup is flushed
    Then the collector received 1 metric point named "runs.started" with the value 1

  @REQ-TELEM-016 @id:TELEM-016-01
  Scenario: A slow collector makes no span wait
    Given the simulated collector answers after 5 seconds of clock time
    When span "step a" is started and ended
    Then ending span "step a" returns without the clock advancing
    And the collector has received 0 spans

  @REQ-TELEM-016 @id:TELEM-016-02
  Scenario: An unreachable collector makes no span wait
    Given the simulated collector is unreachable
    When span "step a" is started and ended
    Then ending span "step a" returns without the clock advancing

  @REQ-TELEM-017 @id:TELEM-017-01
  Scenario: An undelivered batch is dropped and counted
    Given the simulated collector is unreachable
    And run "run_01" of tenant "acme" is running
    When span "step a" is started and ended, and a log record and a metric are recorded
    And the telemetry setup makes 1 export attempt
    Then the counter "telemetry.dropped.spans" is 1
    And the counter "telemetry.dropped.log_records" is 1
    And the counter "telemetry.dropped.metric_points" is 1

  @REQ-TELEM-018 @id:TELEM-018-01
  Scenario: A dropped span is not kept in the cloud profile
    Given the simulated collector is unreachable
    And run "run_01" of tenant "acme" is running
    When span "step a" is started and ended
    And the telemetry setup makes 1 export attempt
    Then the span store holds 0 spans

  @REQ-TELEM-018 @profile:embedded @id:TELEM-018-02
  Scenario: The embedded profile keeps its spans when export fails
    Given an embedded telemetry setup with a fake clock that also exports to an unreachable collector
    And run "run_01" of tenant "acme" is running
    When span "step a" is started and ended
    And the telemetry setup makes 1 export attempt
    Then the span store holds 1 span named "step a"

  @REQ-TELEM-019 @id:TELEM-019-01
  Scenario: A span ends the same way whatever the collector does
    When span "step a" is started and ended while the simulated collector accepts batches
    Then span "step a" ends with status "ok"
    And the collector received 1 span named "step a"

  @REQ-TELEM-019 @id:TELEM-019-02
  Scenario: A run completes when its telemetry is dropped
    Given the simulated collector is unreachable
    And run "run_01" of tenant "acme" is running
    When span "step a" is started and ended
    Then run "run_01" completes with state "completed"

  @REQ-TELEM-020 @security @id:TELEM-020-01
  Scenario: An exported metric point has no labels
    When metric "runs.started" is recorded with the value 1
    And the telemetry setup is flushed
    Then the collector received 1 metric point named "runs.started" with 0 metric labels
