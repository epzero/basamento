@planned @wp:WP-L1-03 @level:component @profile:embedded
Feature: The embedded bus keeps its data across restarts
  In the embedded profile the bus runs in the kit process and stores its data
  in the data directory, next to the SQLite files.

  Background:
    Given the clock is frozen at "2026-11-16T09:00:00Z"

  @REQ-BUS-014 @REQ-BUS-002 @id:BUS-014-01
  Scenario: Messages, entries and bus sequences survive a restart
    Given messages "m1" to "m3" are published on subject "run.r1.events"
    And entry "cursor.r1" is put with value "42"
    When the embedded bus is stopped and started again on the same data directory
    And message "m4" is published on subject "run.r1.events"
    Then a subscriber from the start of "run.r1.events" receives "m1", "m2", "m3", "m4" in that order and nothing else
    And the bus sequence of "m4" is greater than the bus sequence of "m3"
    And reading entry "cursor.r1" returns value "42" with the revision its put returned

  @REQ-BUS-014 @REQ-BUS-018 @id:BUS-014-02
  Scenario: A TTL keeps counting across a restart
    Given entry "lock.r1" is put with value "ana" and TTL "30s"
    When the embedded bus is stopped and started again on the same data directory
    And 30 seconds pass
    Then reading entry "lock.r1" fails with code "not_found"
