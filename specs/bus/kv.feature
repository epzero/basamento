@planned @wp:WP-L1-03 @level:component @profile:embedded @profile:cloud
Feature: Read, put, swap, create and delete KV entries
  The KV store keeps small values such as cursors, locks and presence. Every
  change gives the entry a new revision, and a swap or a create succeeds only
  against the state the caller expects.

  @REQ-BUS-015 @id:BUS-015-01
  Scenario: Each put returns a greater revision and a read returns the latest value
    When entry "cursor.r1" is put with value "1"
    And entry "cursor.r1" is put with value "2"
    Then the second put returned a greater revision than the first
    And reading entry "cursor.r1" returns value "2" with the revision the second put returned

  @REQ-BUS-015 @id:BUS-015-02
  Scenario: Reading a key without an entry fails with not_found
    When entry "cursor.r9" is read
    Then the read fails with code "not_found"

  @REQ-BUS-016 @id:BUS-016-01
  Scenario: A swap at the current revision replaces the value
    Given entry "cursor.r1" is put with value "1"
    When entry "cursor.r1" is swapped to "2" at its current revision
    Then the swap returns a greater revision than the put
    And reading entry "cursor.r1" returns value "2"

  @REQ-BUS-016 @id:BUS-016-02
  Scenario: A swap at an older revision fails and changes nothing
    Given entry "cursor.r1" is put with value "1"
    And entry "cursor.r1" is put with value "2"
    When entry "cursor.r1" is swapped to "3" at the revision the first put returned
    Then the swap fails with code "conflict"
    And reading entry "cursor.r1" returns value "2"

  @REQ-BUS-016 @id:BUS-016-03
  Scenario: A swap of a key without an entry fails with conflict
    When entry "cursor.r9" is swapped to "1" at revision 7
    Then the swap fails with code "conflict"
    And reading entry "cursor.r9" fails with code "not_found"

  @REQ-BUS-016 @id:BUS-016-04
  Scenario: Of concurrent swaps at the same revision exactly one succeeds
    Given entry "owner.r1" is put with value "none"
    When 10 callers concurrently swap entry "owner.r1" to their own name at its current revision
    Then exactly 1 swap succeeds and 9 fail with code "conflict"
    And reading entry "owner.r1" returns the name of the caller whose swap succeeded

  @REQ-BUS-017 @id:BUS-017-01
  Scenario: Creating a key without an entry stores it
    When entry "lock.r1" is created with value "ana"
    Then the create returns a revision
    And reading entry "lock.r1" returns value "ana" with that revision

  @REQ-BUS-017 @id:BUS-017-02
  Scenario: Creating a key that has an entry fails and keeps the entry
    Given entry "lock.r1" is created with value "ana"
    When entry "lock.r1" is created with value "ben"
    Then the create fails with code "conflict"
    And reading entry "lock.r1" returns value "ana"

  @REQ-BUS-017 @REQ-BUS-021 @id:BUS-017-03
  Scenario: A deleted key can be created again
    Given entry "lock.r1" is created with value "ana"
    And entry "lock.r1" is deleted
    When entry "lock.r1" is created with value "ben"
    Then reading entry "lock.r1" returns value "ben"

  @REQ-BUS-021 @id:BUS-021-01
  Scenario: Deleting an entry makes it absent, and deleting it again succeeds
    Given entry "cursor.r1" is put with value "42"
    When entry "cursor.r1" is deleted
    And entry "cursor.r1" is deleted again
    Then both deletes succeed
    And reading entry "cursor.r1" fails with code "not_found"
