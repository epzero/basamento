@wp:WP-L1-07 @level:component @profile:embedded
Feature: The embedded profile keeps a run's spans for the inspector
  In the embedded profile, ended spans go to the span store, and the run
  inspector reads them back as one ordered trace per run. A tenant never
  sees another tenant's spans, and spans do not stay past their retention.

  Background:
    Given an embedded telemetry setup with a fake clock

  @REQ-TELEM-010 @id:TELEM-010-01
  Scenario: An ended span is kept
    Given run "run_01" of tenant "acme" is running
    When span "step a" is started and ended
    Then the span store holds 1 span named "step a"

  @REQ-TELEM-011 @id:TELEM-011-01
  Scenario: A kept span holds what the inspector shows
    Given run "run_01" of tenant "acme" is running
    And the clock is frozen at "2026-10-01T09:00:00Z"
    And span "run_01" is started
    When span "step a" is started with parent span "run_01"
    And 250 milliseconds pass on the clock
    And span "step a" records attribute "attempt" with the value 2
    And span "step a" is ended with status "error"
    Then the kept span "step a" has the trace id of span "run_01"
    And the kept span "step a" has parent span id of span "run_01", name "step a", status "error", run id "run_01" and tenant "acme"
    And the kept span "step a" starts at "2026-10-01T09:00:00Z" and ends at "2026-10-01T09:00:00.250Z"
    And the kept span "step a" has attribute "attempt" with the value 2

  @REQ-TELEM-012 @id:TELEM-012-01
  Scenario: A run's spans come back in start order
    Given run "run_01" of tenant "acme" is running
    When span "step c" is started and ended, then the clock advances by 1 second, then span "step a" is started and ended, then the clock advances by 1 second, then span "step b" is started and ended
    And tenant "acme" reads the spans of run "run_01"
    Then tenant "acme" reads the spans named "step c", "step a", "step b" in that order

  @REQ-TELEM-012 @id:TELEM-012-02
  Scenario: Spans that start together come back in span id order
    Given run "run_01" of tenant "acme" is running
    And the clock is frozen at "2026-10-01T09:00:00Z"
    When spans "left" and "right" are started and ended
    And tenant "acme" reads the spans of run "run_01"
    Then tenant "acme" reads the spans in span id order

  @REQ-TELEM-012 @id:TELEM-012-03
  Scenario: A run's spans leave out the spans of another run
    Given run "run_01" and run "run_02" of tenant "acme" are running
    And span "step a" is started and ended inside run "run_01"
    And the clock advances by 1 second
    And span "step b" is started and ended inside run "run_01"
    And span "step c" is started and ended inside run "run_02"
    When tenant "acme" reads the spans of run "run_01"
    Then tenant "acme" reads 2 spans named "step a", "step b" in that order

  @REQ-TELEM-013 @security @id:TELEM-013-01
  Scenario: A tenant cannot read another tenant's run
    Given run "run_01" of tenant "acme" is running
    When span "step a" is started and ended
    And tenant "globex" reads the spans of run "run_01"
    Then the read returns 0 spans and fails with code "not_found"

  @REQ-TELEM-014 @id:TELEM-014-01
  Scenario: A span older than the retention period is deleted on purge
    Given the span store retention period is 24 hours
    And run "run_01" of tenant "acme" is running
    And span "step a" is started and ended
    When 24 hours and 1 second pass on the clock and the span store purges expired spans
    Then tenant "acme" reads 0 spans of run "run_01"

  @REQ-TELEM-014 @id:TELEM-014-02
  Scenario: A span within the retention period is kept on purge
    Given the span store retention period is 24 hours
    And run "run_01" of tenant "acme" is running
    And span "step a" is started and ended
    When 23 hours and 59 minutes pass on the clock and the span store purges expired spans
    Then tenant "acme" reads 1 span of run "run_01"
