@planned @wp:WP-L1-01 @level:component @profile:embedded @profile:cloud
Feature: Tenant-scoped repositories
  Every repository call carries a tenant, and sees and changes only that
  tenant's records. Another tenant's record looks exactly like one that does
  not exist.

  Background:
    Given a SQL store opened with the sample migrations 1 to 3
    And tenant "acme" has stored items with keys "a1" and "a2"
    And tenant "globex" has stored an item with key "g1"

  @REQ-SQL-010 @security @id:SQL-010-01
  Scenario: A repository call without a tenant is rejected
    When an item with key "orphan" is stored without a tenant
    Then the call fails with code "invalid_argument"
    And no tenant has an item with key "orphan"

  @REQ-SQL-010 @security @id:SQL-010-02
  Scenario: Listing items without a tenant is rejected
    When items are listed without a tenant
    Then the call fails with code "invalid_argument"

  @REQ-SQL-011 @security @id:SQL-011-01
  Scenario: Listing returns only the caller's items
    When tenant "acme" lists its items
    Then the list holds exactly the items with keys "a1", "a2" and "seed"

  @REQ-SQL-011 @security @id:SQL-011-02
  Scenario: Reading another tenant's item by id looks like a missing item
    When tenant "acme" reads the item "g1" of tenant "globex" by its id
    Then the call fails with code "not_found"

  @REQ-SQL-012 @security @id:SQL-012-01
  Scenario: Changing another tenant's item fails and leaves it unchanged
    When tenant "acme" changes the key of the item "g1" of tenant "globex" to "stolen"
    Then the call fails with code "not_found"
    And tenant "globex" has exactly 1 item with key "g1"
    And no tenant has an item with key "stolen"

  @REQ-SQL-012 @security @id:SQL-012-02
  Scenario: Deleting another tenant's item fails and leaves it in place
    When tenant "acme" deletes the item "g1" of tenant "globex" by its id
    Then the call fails with code "not_found"
    And tenant "globex" has exactly 1 item with key "g1"
