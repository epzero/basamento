@journey @level:system
Feature: The runs a person may see
  The inspector lists the runs of the person's projects and shows one run's
  durable events as they arrive. Runs belong to a tenant: a person never sees
  a run of another tenant, one they may not read, or one whose tenant is not
  theirs.

  Background:
    Given "kit serve" runs with the inspector, tenant "acme", and flows "review" and "assistant"
    And run "r1" of "review" is running with 3 events
    And run "r2" of "review" is completed with 5 events
    And run "r3" of "assistant" has state "failed" with error code "vendor_error"

  @planned @wp:WP-L6-04 @security @REQ-INSP-001 @REQ-INSP-002 @id:INSP-001-01
  Scenario: The list holds only the person's own runs
    Given tenant "globex" has run "g1" of their flow "review"
    And account "ana" is in project "p1" of tenant "acme"
    When ana opens the inspector
    Then ana sees the runs "r1", "r2" and "r3", each with its flow and its state
    And ana does not see run "g1" or any run of tenant "globex"

  @planned @wp:WP-L6-04 @REQ-INSP-001 @REQ-INSP-002 @id:INSP-001-02
  Scenario: Another project of the same tenant is not listed
    Given account "ana" is in project "p1" of tenant "acme"
    And run "r4" of "review" is in project "p2" of tenant "acme"
    When ana opens the inspector
    Then ana sees the runs "r1", "r2" and "r3"
    And ana does not see run "r4"

  @planned @wp:WP-L6-04 @security @REQ-INSP-013 @id:INSP-013-01
  Scenario: A person without the permission to read a run sees nothing of it
    Given account "vic" is a "viewer" in project "p1" of tenant "acme" without the permission to read runs
    When vic opens run "r2"
    Then vic is denied access to run "r2"
    And vic sees no event, gate or state of run "r2"

  @planned @wp:WP-L6-04 @security @REQ-INSP-002 @id:INSP-002-01
  Scenario: A link to another tenant's run is denied without revealing it
    Given account "ana" is in project "p1" of tenant "acme"
    When ana opens a link to run "g1" of tenant "globex"
    Then ana is denied access to run "g1"
    And ana sees no flow, state, event or gate of run "g1"

  @planned @wp:WP-L6-04 @REQ-INSP-004 @id:INSP-004-01
  Scenario: Opening a past run shows its events in order
    When ana opens run "r2"
    Then ana sees the 5 events of run "r2" in sequence order
    And the state ana sees for run "r2" is "completed"

  @planned @wp:WP-L6-04 @REQ-INSP-003 @id:INSP-003-01
  Scenario: A live run gains events while it is open
    When ana opens run "r1"
    And the run adds 2 more events
    Then ana sees the 5 events of run "r1" in sequence order

  @planned @wp:WP-L6-04 @REQ-INSP-003 @REQ-INSP-004 @id:INSP-003-02
  Scenario: Reloading a live run shows what happened meanwhile
    Given ana left the inspector after opening run "r1"
    And the run adds 2 more events
    When ana opens run "r1" again
    And the run adds 1 more event
    Then ana sees the 6 events of run "r1" in sequence order

  @planned @wp:WP-L6-04 @REQ-INSP-008 @id:INSP-008-01
  Scenario: A failed run shows its state and error code
    When ana opens run "r3"
    Then ana sees the state "failed" for run "r3"
    And ana sees the error code "vendor_error" for run "r3"

  @planned @wp:WP-L6-04 @REQ-INSP-009 @id:INSP-009-01
  Scenario: A cancelled run is shown as cancelled, with no error code
    Given run "r5" of "assistant" has state "cancelled"
    When ana opens run "r5"
    Then ana sees the state "cancelled" for run "r5"
    And ana sees no error code for run "r5"

  @planned @wp:WP-L6-04 @REQ-INSP-008 @id:INSP-008-02
  Scenario: A run that fails while open is shown as failed
    Given ana has opened run "r1"
    When run "r1" fails with error code "budget_exceeded"
    Then ana sees the state "failed" for run "r1"
    And ana sees the error code "budget_exceeded" for run "r1"
