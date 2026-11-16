@planned @wp:WP-L1-01 @level:component
Feature: Opening a SQL store
  The SQL store is chosen by URL: a SQLite file in the embedded profile,
  Postgres in the cloud profile. A wrong or unreachable URL fails fast with a
  stable code, and never leaks the password it carries. In URLs, "{data}"
  stands for the path of the scenario's data directory.

  @REQ-SQL-001 @profile:embedded @id:SQL-001-01
  Scenario: A SQLite URL in an empty data directory creates the database
    Given an empty data directory
    When a SQL store is opened with URL "sqlite:///{data}/kit.db" and the sample migrations 1 to 3
    Then the store opens
    And the data directory contains the file "kit.db"
    And the schema version is 3

  @REQ-SQL-002 @id:SQL-002-01
  Scenario Outline: Unsupported URLs are rejected without creating anything
    Given an empty data directory
    When a SQL store is opened with URL "<url>" and the sample migrations 1 to 3
    Then opening fails with code "invalid_argument"
    And the data directory is still empty

    Examples:
      | url                                |
      | mysql://kit@db.example.test/kit    |
      | file:///{data}/kit.db              |
      | sqlite://                          |
      | postgres://kit@db.example.test     |
      | kit.db                             |

  @REQ-SQL-003 @profile:cloud @id:SQL-003-01
  Scenario: An unreachable Postgres server fails as retryable
    Given a database server address where nothing listens
    When a SQL store is opened with a "postgres" URL for that address and the sample migrations 1 to 3
    Then opening fails with code "unavailable"
    And the error is marked retryable

  @REQ-SQL-004 @security @id:SQL-004-01
  Scenario Outline: A failed open never reveals the URL's password
    Given a database server address where nothing listens
    When a SQL store is opened with a "<scheme>" URL for that address with user "kit" and password "s3cret-pw"
    Then opening fails with code "<code>"
    And neither the error message nor its details contain "s3cret-pw"

    Examples:
      | scheme   | code             |
      | postgres | unavailable      |
      | mysql    | invalid_argument |
