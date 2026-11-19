@wp:WP-L8-02 @security @level:component
Feature: Permission checks

  @REQ-AUTH-004 @id:AUTH-004-01
  Scenario: A viewer cannot cancel a run
    Given principal "vic" has role "viewer" in project "p1"
    When vic cancels a run in project "p1"
    Then the action fails with code "permission_denied"
    And an audit event records the denied action by vic
