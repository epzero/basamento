@planned @wp:WP-L1-03 @level:component @profile:embedded @profile:cloud
Feature: Limits, invalid arguments and an unreachable server
  Calls the bus or the KV store cannot honor fail at once with a stable error
  code and leave nothing behind.

  @REQ-BUS-011 @id:BUS-011-01
  Scenario Outline: Messages up to the maximum size are stored, larger ones refused
    Given the maximum message size is 1024 bytes
    When a message of <size> bytes is published on subject "run.r1.deltas"
    Then the publish <result>
    And a subscriber from the start of "run.r1.deltas" receives <count> messages

    Examples:
      | size | result                              | count |
      | 1024 | returns a bus sequence              | 1     |
      | 1025 | fails with code "invalid_argument"  | 0     |

  @REQ-BUS-012 @id:BUS-012-01
  Scenario Outline: A malformed subject is refused
    When message "m1" is published on subject "<subject>"
    Then the publish fails with code "invalid_argument"

    Examples:
      | subject      |
      |              |
      | run.*.deltas |
      | run.>        |
      | run..deltas  |
      | run r1       |

  @REQ-BUS-012 @id:BUS-012-02
  Scenario Outline: A malformed key or a negative TTL is refused
    When entry "<key>" is put with value "v" and TTL "<ttl>"
    Then the put fails with code "invalid_argument"
    And the KV store holds no entry

    Examples:
      | key       | ttl |
      |           | 0s  |
      | lock r1   | 0s  |
      | lock.*    | 0s  |
      | lock.>    | 0s  |
      | lock.r1   | -1s |

  @REQ-BUS-013 @id:BUS-013-01
  Scenario: Calls fail with unavailable while the bus server is down
    Given the bus server is stopped
    When message "m1" is published on subject "run.r1.deltas"
    And entry "lock.r1" is read
    Then the publish fails with code "unavailable"
    And the read fails with code "unavailable"
