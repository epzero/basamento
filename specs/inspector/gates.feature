@journey @level:system
Feature: Open gates and their resolution
  A run that waits at a gate shows it in the inspector, with the payload the
  gate accepts. A person resolves it from the inspector; a payload the gate
  does not accept is refused and explained, and the gate stays open. The
  validation itself belongs to the flow engine (REQ-FLOW-006).

  Background:
    Given "kit serve" runs with the inspector, tenant "acme", and flow "review"
    And account "ana" is in project "p1" of tenant "acme"
    And flow "review" has gates "approve" and "review"

  @wp:WP-L6-04 @REQ-INSP-005 @id:INSP-005-01
  Scenario: A waiting run lists its open gate with what it accepts
    Given run "r1" of "review" waits at gate "approve", which accepts payloads where "input.approved || size(input.notes) > 0"
    When ana opens run "r1"
    Then ana sees 1 open gate, "approve", for run "r1"
    And ana sees that gate "approve" accepts payloads where "input.approved || size(input.notes) > 0"

  @wp:WP-L6-04 @REQ-INSP-005 @id:INSP-005-02
  Scenario: A run that does not wait at a gate lists none
    Given run "r2" of "review" is running
    When ana opens run "r2"
    Then ana sees 0 open gates for run "r2"

  @wp:WP-L6-04 @REQ-INSP-005 @id:INSP-005-03
  Scenario: A gate that opens while the run is shown is listed
    Given run "r2" of "review" is running
    And ana has opened run "r2"
    When run "r2" reaches gate "review", which accepts payloads where "input.approved"
    Then ana sees 1 open gate, "review", for run "r2"

  @wp:WP-L6-04 @REQ-INSP-005 @id:INSP-005-04
  Scenario: A run waiting at two gates lists both open gates
    Given run "r1" of "review" waits at gate "approve", which accepts payloads where "input.approved"
    And run "r1" of "review" waits at gate "review", which accepts payloads where "size(input.notes) > 0"
    When ana opens run "r1"
    Then ana sees 2 open gates, "approve" and "review", for run "r1"
    And ana sees that gate "approve" accepts payloads where "input.approved"
    And ana sees that gate "review" accepts payloads where "size(input.notes) > 0"

  @wp:WP-L6-04 @REQ-INSP-006 @id:INSP-006-01
  Scenario: A person resolves a gate with an accepted payload
    Given run "r1" of "review" waits at gate "approve", which accepts payloads where "input.approved || size(input.notes) > 0"
    When ana resolves gate "approve" of run "r1" with payload {"approved": true}
    Then ana sees 0 open gates for run "r1"
    And run "r1" continues past gate "approve" with payload {"approved": true}

  @wp:WP-L6-04 @REQ-INSP-007 @id:INSP-007-01
  Scenario: A payload the gate does not accept is refused and explained
    Given run "r1" of "review" waits at gate "approve", which accepts payloads where "input.approved || size(input.notes) > 0"
    When ana resolves gate "approve" of run "r1" with payload {"approved": false, "notes": ""}
    Then ana sees that gate "approve" rejected the payload with code "invalid_argument"
    And ana still sees 1 open gate, "approve", for run "r1"
    And run "r1" still waits at gate "approve"

  @wp:WP-L6-04 @REQ-INSP-007 @id:INSP-007-02
  Scenario: A rejected payload is followed by an accepted one
    Given run "r1" of "review" waits at gate "approve", which accepts payloads where "input.approved || size(input.notes) > 0"
    And ana resolved gate "approve" of run "r1" with payload {"approved": false, "notes": ""} and it was rejected
    When ana resolves gate "approve" of run "r1" with payload {"approved": false, "notes": "looks right"}
    Then ana sees 0 open gates for run "r1"

