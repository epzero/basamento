@wp:WP-L5-04 @runtime:any @level:component
Feature: Runs pin their definition and validate gates

  @REQ-FLOW-005 @id:FLOW-005-01
  Scenario: A running run keeps its definition after an edit
    Given flow "review" version 1 waits at gate "approve"
    And a run of "review" is waiting at gate "approve"
    When flow "review" is edited to version 2 with a different next node
    And the gate "approve" is resolved
    Then the run follows version 1

  @REQ-FLOW-006 @id:FLOW-006-01
  Scenario: An invalid gate payload is rejected and the gate stays open
    Given a run waiting at gate "approve" that accepts payloads where "input.approved || size(input.notes) > 0"
    When the gate is resolved with payload {"approved": false, "notes": ""}
    Then the resolution fails with code "invalid_argument"
    And the run is still waiting at gate "approve"
