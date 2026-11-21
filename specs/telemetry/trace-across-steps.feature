@wp:WP-L1-07 @level:component
Feature: Trace context crosses a boundary
  When trace context is written to a carrier and read back, a child span started
  from the restored context joins the parent trace.

  Background:
    Given an embedded telemetry setup with a fake clock

  @REQ-TELEM-021 @id:TELEM-021-01
  Scenario: A child span started from a restored trace context joins the parent trace
    Given run "run_01" of tenant "acme" is running
    And span "run_01" is started
    When the trace context of span "run_01" is written to a carrier and read back
    And span "step a" is started from the restored trace context and ended
    And span "run_01" is ended
    Then the kept span "step a" has the trace id of span "run_01"
    And the kept span "step a" has parent span id of span "run_01"
