@wp:WP-L1-03 @level:component @profile:embedded @profile:cloud @REQ-BUS-001
Feature: KV entries that expire after a TTL
  A holder keeps a lock or a presence entry by renewing it before its TTL
  passes. Expiry follows the injected clock, so it is exact and repeatable;
  once an entry expires, its holder has lost it.

  Background:
    Given the clock is frozen at "2026-11-16T09:00:00Z"

  @REQ-BUS-018 @id:BUS-018-01
  Scenario Outline: An entry expires exactly when its TTL has passed
    Given entry "lock.r1" is put with value "ana" and TTL "30s"
    When <elapsed> pass
    Then reading entry "lock.r1" <outcome>

    Examples:
      | elapsed            | outcome                          |
      | 29999 milliseconds | returns value "ana"              |
      | 30 seconds         | fails with code "not_found"      |

  @REQ-BUS-023 @id:BUS-023-01
  Scenario: An entry without a TTL does not expire
    Given entry "cursor.r1" is put with value "42"
    When 365 days pass
    Then reading entry "cursor.r1" returns value "42"

  @REQ-BUS-019 @id:BUS-019-01
  Scenario: Anyone can create a key whose entry has expired
    Given entry "lock.r1" is created with value "ana" and TTL "30s"
    When 30 seconds pass
    And entry "lock.r1" is created with value "ben" and TTL "30s"
    Then the create returns a revision
    And reading entry "lock.r1" returns value "ben"

  @REQ-BUS-025 @id:BUS-025-01
  Scenario: A holder cannot renew an expired entry, even if nobody took it
    Given entry "lock.r1" is created with value "ana" and TTL "30s"
    When 30 seconds pass
    And entry "lock.r1" is swapped to "ana" with TTL "30s" at the revision the create returned
    Then the swap fails with code "conflict"
    And reading entry "lock.r1" fails with code "not_found"

  @REQ-BUS-025 @REQ-BUS-016 @id:BUS-025-02
  Scenario: A holder cannot renew after another holder took the key
    Given entry "lock.r1" is created with value "ana" and TTL "30s"
    And 30 seconds pass
    And entry "lock.r1" is created with value "ben" and TTL "30s"
    When entry "lock.r1" is swapped to "ana" with TTL "30s" at the revision the first create returned
    Then the swap fails with code "conflict"
    And reading entry "lock.r1" returns value "ben"

  @REQ-BUS-020 @id:BUS-020-01
  Scenario: A renewal counts the TTL from the renewal
    Given entry "lock.r1" is created with value "ana" and TTL "30s"
    And 20 seconds pass
    When entry "lock.r1" is swapped to "ana" with TTL "30s" at its current revision
    And 29 seconds pass
    Then reading entry "lock.r1" returns value "ana"
    When 1 second passes
    Then reading entry "lock.r1" fails with code "not_found"
