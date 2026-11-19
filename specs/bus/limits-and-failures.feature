@planned @wp:WP-L1-03 @level:component @profile:embedded @profile:cloud @REQ-BUS-001
Feature: Limits, invalid arguments, an unreachable server, corrupt records and cancelled calls
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

    Examples:
      | key       | ttl |
      |           | 0s  |
      | lock r1   | 0s  |
      | lock.*    | 0s  |
      | lock.>    | 0s  |
      | .lock     | 0s  |
      | lock.     | 0s  |
      | lock.r1   | -1s |

  @REQ-BUS-012 @id:BUS-012-03
  Scenario Outline: A key with an empty dot-separated token is refused
    When entry "<key>" is put with value "v" and TTL "0s"
    Then the put fails with code "invalid_argument"

    Examples:
      | key      |
      | lock..r1 |

  @REQ-BUS-013 @id:BUS-013-01
  Scenario: Calls fail with unavailable while the bus server is down
    Given the bus server is stopped
    When message "m1" is published on subject "run.r1.deltas"
    And entry "lock.r1" is read
    Then the publish fails with code "unavailable"
    And the read fails with code "unavailable"

  @REQ-BUS-026 @security @id:BUS-026-01
  Scenario: A KV record the store did not write fails reads, swaps and creates with internal
    Given the entry "cursor.r1" is overwritten outside the store with "T0K"
    When entry "cursor.r1" is read
    And entry "cursor.r1" is swapped to "2" at revision 1
    And entry "cursor.r1" is created with value "2"
    Then the read fails with code "internal"
    And the swap fails with code "internal"
    And the create fails with code "internal"
    And none of the errors contains "cursor.r1" or "T0K"

  @REQ-BUS-027 @id:BUS-027-01
  Scenario Outline: A call whose context is cancelled fails with cancelled
    When the bus receives call "<call>" on subject "run.r1.deltas" and key "cursor.r1" with a cancelled context
    Then the call fails with code "cancelled"
    And the subject and the key hold no message and no entry

    Examples:
      | call                     |
      | Publish                  |
      | Subscribe from the start |
      | LastSeq                  |
      | Purge                    |
      | Get                      |
      | CompareAndSwap           |
      | Delete                   |

  @REQ-BUS-028 @id:BUS-028-01
  Scenario Outline: A call whose context deadline has passed fails with timeout
    When the bus receives call "<call>" on subject "run.r1.deltas" and key "cursor.r1" with a context whose deadline has passed
    Then the call fails with code "timeout"
    And the subject and the key hold no message and no entry

    Examples:
      | call                     |
      | Publish                  |
      | Subscribe from the start |
      | LastSeq                  |
      | Purge                    |
      | Get                      |
      | CompareAndSwap           |
      | Delete                   |

  @REQ-BUS-029 @security @id:BUS-029-01
  Scenario Outline: A cloud URL with credentials over plain nats is accepted only on loopback
    When the cloud bus opens on "<url>"
    Then the open <result>

    Examples:
      | url                                           | result                                                                  |
      | nats://kit-s3cret@10.0.0.1:4222               | fails with code "invalid_argument" without "kit-s3cret" or "10.0.0.1"   |
      | nats://kit-s3cret@nats.example.test:4222      | fails with code "invalid_argument" without "kit-s3cret" or "example.test" |
      | nats://127.0.0.1:1                            | fails with code "unavailable"                                           |
      | nats://[::1]:1                                | fails with code "unavailable"                                           |
      | nats://localhost:1                            | fails with code "unavailable"                                           |
      | nats://10.0.0.1:1                             | fails with code "unavailable"                                           |

  @REQ-BUS-032 @id:BUS-032-01
  Scenario Outline: A KV value at the bound is stored, one byte over is refused
    Given the maximum message size is 1024 bytes
    And entry "cursor.r1" is put with value "seed"
    When entry "cursor.r1" is <call> a value of <size> bytes
    Then the <call> <result>
    And reading entry "cursor.r1" <stored>

    Examples:
      | call | size | result                             | stored                                   |
      | put  | 1016 | succeeds                           | returns 1016 bytes                       |
      | put  | 1017 | fails with code "invalid_argument" | returns the 4 bytes it was put with last |
      | swap | 1016 | succeeds                           | returns 1016 bytes                       |
      | swap | 1017 | fails with code "invalid_argument" | returns the 4 bytes of "seed"            |
