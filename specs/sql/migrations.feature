@wp:WP-L1-01 @level:component @profile:embedded @profile:cloud
Feature: Migrations from the binary
  Every SQL store migrates its database on open, from the migrations embedded
  in the binary. Upgrades keep existing data, a failed migration leaves no
  trace, and an older binary never touches a newer database. The sample
  migrations are described in design.md: migration 2 stores the item "seed"
  for tenant "acme".

  @REQ-SQL-005 @id:SQL-005-01
  Scenario: An empty database is migrated to the newest version
    Given an empty database
    When a SQL store is opened with the sample migrations 1 to 3
    Then the store opens
    And the schema version is 3
    And tenant "acme" has exactly 1 item with key "seed"

  @REQ-SQL-005 @id:SQL-005-02
  Scenario: An upgrade applies only the newer migrations and keeps existing data
    Given a database migrated with the sample migrations 1 to 2
    And tenant "acme" has stored an item with key "alpha"
    When a SQL store is opened with the sample migrations 1 to 3
    Then the schema version is 3
    And tenant "acme" has exactly 1 item with key "seed"
    And tenant "acme" has exactly 1 item with key "alpha"

  @REQ-SQL-006 @id:SQL-006-01
  Scenario: Reopening an up-to-date database applies nothing
    Given a database migrated with the sample migrations 1 to 3
    And tenant "acme" has deleted its item with key "seed"
    When a SQL store is opened with the sample migrations 1 to 3
    Then the store opens
    And the schema version is 3
    And tenant "acme" has no item with key "seed"

  @REQ-SQL-007 @id:SQL-007-01
  Scenario: A failed migration leaves no trace
    Given a database migrated with the sample migrations 1 to 3
    When a SQL store is opened with the sample migrations 1 to 3 and the broken migration 4
    Then opening fails with code "internal"
    And the schema version is 3
    And tenant "acme" has no item with key "seeded-by-4"

  @REQ-SQL-008 @id:SQL-008-01
  Scenario: An older binary refuses a newer database
    Given a database migrated with the sample migrations 1 to 3
    When a SQL store is opened with the sample migrations 1 to 2
    Then opening fails with code "unsupported"
    And the schema version is 3

  @REQ-SQL-009 @id:SQL-009-01
  Scenario: Two stores opening one empty database at once migrate it once
    Given an empty database
    When 2 SQL stores open it at the same time with the sample migrations 1 to 3
    Then both stores open
    And the schema version is 3
    And tenant "acme" has exactly 1 item with key "seed"
