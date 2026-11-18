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

  @REQ-SQL-001 @profile:cloud @id:SQL-001-02
  Scenario: A Postgres URL opens the database
    Given an empty database
    When a SQL store is opened with the sample migrations 1 to 3
    Then the store opens
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

  @REQ-SQL-019 @profile:embedded @id:SQL-019-01
  Scenario: A relative SQLite path is resolved against the working directory
    Given an empty data directory
    When the working directory is the data directory
    And a SQL store is opened with URL "sqlite://data/kit.db" and the sample migrations 1 to 3
    Then the store opens
    And the data directory contains the file "data/kit.db"
    And the schema version is 3

  @REQ-SQL-020 @id:SQL-020-01
  Scenario Outline: SQLite URLs with a query or fragment and the legacy scheme are rejected
    Given an empty data directory
    When a SQL store is opened with URL "<url>" and the sample migrations 1 to 3
    Then opening fails with code "invalid_argument"
    And the data directory is still empty

    Examples:
      | url                                       |
      | sqlite:///{data}/kit.db?cache=shared      |
      | sqlite:///{data}/kit.db#main              |
      | postgresql://kit@db.example.test/kit      |

  @REQ-SQL-021 @profile:embedded @id:SQL-021-01
  Scenario: An in-memory SQLite database is never made a file
    Given an empty data directory
    When the working directory is the data directory
    And a SQL store is opened with URL "sqlite://:memory:" and the sample migrations 1 to 3
    Then the store opens
    And the schema version is 3
    And the data directory is still empty

  @REQ-SQL-024 @security @profile:cloud @id:SQL-024-01
  Scenario: The password of a Postgres URL is redacted from an error
    Given a database server address where nothing listens
    When a SQL store is opened with a "postgres" URL for that address with user "kit" and password "s3cret-pw"
    Then opening fails with code "unavailable"
    And the error carries a URL that does not contain "s3cret-pw"

  @REQ-SQL-024 @security @profile:cloud @id:SQL-024-02
  Scenario Outline: Credential query parameters are redacted from an error
    Given a database server address where nothing listens
    When a SQL store is opened with a "postgres" URL for that address with the query "<query>"
    Then opening fails with code "unavailable"
    And the error carries a URL that does not contain "<secret>"

    Examples:
      | query                                   | secret       |
      | password=s3cret-pw                      | s3cret-pw    |
      | sslpassword=ssl-secret                  | ssl-secret   |

  @REQ-SQL-025 @security @profile:cloud @id:SQL-025-01
  Scenario: Driver text is not forwarded when the URL carried a secret
    Given a database server address where nothing listens
    When a SQL store is opened with a "postgres" URL for that address with user "kit" and password "s3cret-pw"
    Then opening fails with code "unavailable"
    And the error's text does not name a driver cause

