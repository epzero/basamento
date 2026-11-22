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

  @planned @wp:WP-L1-01 @REQ-SQL-026 @id:SQL-026-01
  Scenario: Two packages with the same local migration number both migrate one database
    Given an empty database
    And a migration set "credentials" in band 1 with local migration 1 creating table "credentials"
    And a migration set "eventlog" in band 2 with local migration 1 creating table "eventlog"
    When a SQL store is opened on the database with the two migration sets composed into one track
    Then the store opens
    And the schema version is 2000001
    And the applied migration versions are 1000001 and 2000001
    And the database has a table "credentials"
    And the database has a table "eventlog"

  @planned @wp:WP-L1-01 @REQ-SQL-026 @id:SQL-026-02
  Scenario: Two migration sets in one band are refused before any migration runs
    Given an empty database
    And a migration set "credentials" in band 1 with local migration 1 creating table "credentials"
    And a migration set "eventlog" in band 1 with local migration 1 creating table "eventlog"
    When a SQL store is opened on the database with the two migration sets composed into one track
    Then opening fails with code "internal"
    And the error message names "credentials", "eventlog" and band 1
    And the database has no table "credentials"
    And the database has no table "eventlog"

  @planned @wp:WP-L1-01 @REQ-SQL-027 @id:SQL-027-01
  Scenario: A migration added to an earlier band is applied after the higher bands
    Given a migration set "credentials" in band 1 with local migration 1 creating table "credentials"
    And a migration set "eventlog" in band 2 with local migration 1 creating table "eventlog"
    And a migration set "authz" in band 3 with local migration 1 creating table "authz"
    And a database migrated with all three composed migration sets
    And local migration 2 is added to "credentials", creating table "credentials_extra"
    When a SQL store is opened on the database with the composed migration sets including the new migration
    Then the store opens
    And the database has a table "credentials_extra"
    And the applied migration versions are 1000001, 1000002, 2000001 and 3000001

  @planned @wp:WP-L1-01 @REQ-SQL-028 @id:SQL-028-01
  Scenario: A Postgres-only fragment runs on Postgres and is ignored on SQLite
    Given a migration set "credentials" in band 1 with local migration 1 creating table "credentials"
    And a Postgres-only fragment of local migration 2 of "credentials" creating table "credentials_pg"
    And an empty database
    When a SQL store is opened on the database with the composed migration set
    Then the store opens
    And the database has a table "credentials"
    And on SQLite the database has no table "credentials_pg"
    And on Postgres the database has a table "credentials_pg"
    And on SQLite the applied migration versions are 1000001
    And on Postgres the applied migration versions are 1000001 and 1000002

  @planned @wp:WP-L1-01 @REQ-SQL-029 @id:SQL-029-01
  Scenario: A database carrying a version the binary does not carry is refused
    Given a migration set "credentials" in band 1 with local migration 1 creating table "credentials", 2 creating table "credentials_removed" and 3 creating table "credentials_new"
    And a database migrated with that migration set
    When a SQL store is opened on the database with a composed migration set that no longer contains local migration 2
    Then opening fails with code "unsupported"
    And the error message is "sql: database schema has a migration this binary does not carry"
    And the database still has a table "credentials_removed"
    And the applied migration versions are 1000001, 1000002 and 1000003
