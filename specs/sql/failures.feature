@wp:WP-L1-01 @level:component
Feature: Failures of the SQL store on both dialects
  Constraint violations and database failures map to the same stable codes on
  SQLite and Postgres, so a caller can retry what is retryable and fix what is
  invalid input.

  Background:
    Given a SQL store opened with the sample migrations 1 to 3

  @REQ-SQL-022 @profile:embedded @profile:cloud @id:SQL-022-01
  Scenario Outline: A not-null or check violation is rejected as invalid input
    When tenant "acme" stores an item with key "<key>" violating the "<rule>" constraint
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no item with key "<key>"

    Examples:
      | key    | rule     |
      | k-null | not-null |
      | k-tag  | check    |

  @REQ-SQL-023 @profile:embedded @id:SQL-023-01
  Scenario: A busy SQLite database is retryable
    When tenant "acme" writes an item while the database refuses with "busy"
    Then the call fails with code "unavailable"
    And the error is marked retryable

  @REQ-SQL-023 @profile:cloud @id:SQL-023-02
  Scenario Outline: Deadlocks, serialization failures and resource limits are retryable
    When tenant "acme" writes an item while the database refuses with "<refusal>"
    Then the call fails with code "unavailable"
    And the error is marked retryable

    Examples:
      | refusal               |
      | serialization failure |
      | deadlock              |
      | out of resources      |

  @REQ-SQL-023 @profile:cloud @id:SQL-023-03
  Scenario: A call fails as retryable when the database becomes unreachable
    When the database becomes unreachable
    And tenant "acme" stores an item with key "k1"
    Then the call fails with code "unavailable"
    And the error is marked retryable
