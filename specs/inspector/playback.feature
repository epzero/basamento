@journey @level:system
Feature: Playback of a browser session recording
  Where a run includes a browser session recorded with rrweb, the inspector
  replays the recording with rrweb-player (docs/architecture/browser-runner.md
  §3). A run without one, and one whose recording cannot be loaded, are told
  so plainly.

  Background:
    Given "kit serve" runs with the inspector, tenant "acme", and flow "checkout"
    And account "ana" is in project "p1" of tenant "acme"

  @wp:WP-L6-04 @REQ-INSP-010 @id:INSP-010-01
  Scenario: A person replays a run's browser session
    Given run "r1" of "checkout" has an rrweb recording of 40 events
    When ana opens run "r1" and replays its recording
    Then ana sees the 40 events of the rrweb recording replayed in order
    And ana sees that the replay has stopped at the end of the recording

  @wp:WP-L6-04 @REQ-INSP-011 @id:INSP-011-01
  Scenario: A run without a browser session has nothing to replay
    Given run "r2" of "checkout" has no rrweb recording
    When ana opens run "r2"
    Then ana sees that run "r2" has no recording to replay

  @wp:WP-L6-04 @REQ-INSP-012 @id:INSP-012-01
  Scenario: A recording that cannot be loaded is explained
    Given run "r4" of "checkout" has an rrweb recording that cannot be loaded
    When ana opens run "r4"
    Then ana sees that the recording of run "r4" cannot be replayed
