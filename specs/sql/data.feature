@planned @wp:WP-L1-01 @level:component @profile:embedded @profile:cloud
Feature: Transactions, constraints and values on both dialects
  Code is written once for SQLite and Postgres. Transactions, constraint
  violations, timestamps and text behave the same on both, including where
  the two databases differ by default.

  Background:
    Given a SQL store opened with the sample migrations 1 to 3

  @REQ-SQL-013 @id:SQL-013-01
  Scenario: A failed transaction leaves none of its writes
    When tenant "acme" runs a transaction that stores items with keys "t1" and "t2" and then fails
    Then the transaction fails with the error of its work
    And tenant "acme" has no item with key "t1"
    And tenant "acme" has no item with key "t2"

  @REQ-SQL-013 @id:SQL-013-02
  Scenario: A successful transaction keeps all of its writes
    When tenant "acme" runs a transaction that stores items with keys "t1" and "t2"
    Then the transaction succeeds
    And tenant "acme" has exactly 1 item with key "t1"
    And tenant "acme" has exactly 1 item with key "t2"

  @REQ-SQL-014 @id:SQL-014-01
  Scenario: A duplicate key is a conflict
    Given tenant "acme" has stored an item with key "k1" and note "first"
    When tenant "acme" stores an item with key "k1" and note "second"
    Then the call fails with code "conflict"
    And the item of tenant "acme" with key "k1" has note "first"

  @REQ-SQL-015 @id:SQL-015-01
  Scenario: An item cannot be put in a list that does not exist
    When tenant "acme" stores an item with key "k1" in a list that does not exist
    Then the call fails with code "conflict"
    And tenant "acme" has no item with key "k1"

  @REQ-SQL-015 @id:SQL-015-02
  Scenario: A list that still holds items cannot be deleted
    Given tenant "acme" has a list "groceries" holding the item with key "k1"
    When tenant "acme" deletes the list "groceries"
    Then the call fails with code "conflict"
    And tenant "acme" has a list "groceries" holding the item with key "k1"

  @REQ-SQL-016 @id:SQL-016-01
  Scenario Outline: Timestamps come back in UTC, truncated to the microsecond
    When tenant "acme" stores an item with key "k1" at "<stored>"
    Then the item of tenant "acme" with key "k1" is at "<read>"

    Examples:
      | stored                              | read                        |
      | 2026-10-01T09:00:00.123456789+02:00 | 2026-10-01T07:00:00.123456Z |
      | 2026-10-01T09:00:00.9999999Z        | 2026-10-01T09:00:00.999999Z |
      | 2026-10-01T09:00:00Z                | 2026-10-01T09:00:00Z        |

  @REQ-SQL-017 @id:SQL-017-01
  Scenario: Items listed by key are in byte order
    Given tenant "acme" has stored items with keys "b", "é", "B", "a" and "A"
    When tenant "acme" lists its items ordered by key
    Then the keys are, in order, "A", "B", "a", "b", "seed" and "é"

  @REQ-SQL-018 @id:SQL-018-01
  Scenario: Text with a NUL character is rejected
    When tenant "acme" stores an item with key "k1" and a note containing a NUL character
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no item with key "k1"
