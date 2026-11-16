@planned @wp:WP-L1-03 @level:component
Feature: The same results on every bus and KV implementation
  Callers write against the ports. The in-process fake that unit tests use,
  the embedded implementation and the cloud implementation must not differ in
  anything a scenario of this capability can observe.

  @REQ-BUS-001 @id:BUS-001-01
  Scenario Outline: A publish, a replay and two creates give the same results everywhere
    Given the bus implementation is "<implementation>"
    And the clock is frozen at "2026-11-16T09:00:00Z"
    When messages "m1" to "m3" are published on subject "run.r1.events"
    And entry "lock.r1" is created with value "ana" and TTL "30s"
    And entry "lock.r1" is created with value "ben" and TTL "30s"
    Then a subscriber from the start of "run.r1.events" receives "m1", "m2", "m3" in that order and nothing else
    And the second create fails with code "conflict"
    And reading entry "lock.r1" returns value "ana"

    Examples:
      | implementation |
      | fake           |
      | embedded       |
      | cloud          |
