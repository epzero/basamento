@planned @wp:WP-L1-07 @level:component @runtime:any
Feature: Trace context crosses a step boundary
  A step receives the trace of the run it belongs to, so its span joins the
  run's trace after a restart and in every attempt.

  Background:
    Given an embedded telemetry setup with a fake clock

  @REQ-TELEM-021 @id:TELEM-021-01
  Scenario: A step's span joins the run's trace
    Given run "run_01" of tenant "acme" is running inside span "run_01"
    When step "charge" runs and records span "charge"
    And span "run_01" is ended
    And tenant "acme" reads the spans of run "run_01"
    Then the kept span "charge" has the trace id of span "run_01"

  @REQ-TELEM-021 @id:TELEM-021-02
  Scenario: A step's span joins the run's trace after a restart
    Given run "run_01" of tenant "acme" is running inside span "run_01"
    And step "charge" has been recorded
    When the worker restarts
    And step "charge" runs again and records span "charge"
    And span "run_01" is ended
    And tenant "acme" reads the spans of run "run_01"
    Then the kept span "charge" has the trace id of span "run_01"

  @REQ-TELEM-021 @id:TELEM-021-03
  Scenario: Every attempt of a step joins the run's trace
    Given run "run_01" of tenant "acme" is running inside span "run_01"
    When step "charge" is recorded, then attempted again
    And span "run_01" is ended
    And tenant "acme" reads the spans of run "run_01"
    Then the kept spans "charge" both have the trace id of span "run_01"
    And run "run_01" has 2 kept spans named "charge"
