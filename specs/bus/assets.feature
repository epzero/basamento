@planned @wp:WP-L1-03 @level:component @profile:cloud
Feature: Streams and buckets the bus can use
  The bus shares a JetStream cluster with other clients. It creates what it
  needs when it finds nothing, accepts what already matches its configuration,
  and refuses anything else without changing it.

  @REQ-BUS-030 @id:BUS-030-01
  Scenario: A matching JetStream setup the bus did not create is used as it is
    Given a second client created the JetStream assets the bus requires on the cloud test server
    When the cloud bus opens on the cloud test server
    Then the open succeeds
    And message "m1" is published on subject "run.r1.deltas"
    And a subscriber to "run.r1.deltas" from the start receives "m1" and nothing else

  @REQ-BUS-030 @contract @id:BUS-030-02
  Scenario Outline: A stream or bucket the bus cannot use is refused and left unchanged
    Given a second client created the bus asset "<asset>" on the cloud test server as "<fault>"
    When the cloud bus opens on the cloud test server
    Then the open fails with code "invalid_argument" with detail "asset" set to "<asset>"
    And the bus asset "<asset>" is still configured as "<fault>"

    Examples:
      | asset   | fault                |
      | KIT_BUS | a mirror             |
      | KIT_BUS | a source             |
      | KIT_BUS | work-queue retention |
      | KIT_KV  | a source             |

  @REQ-BUS-030 @id:BUS-030-03
  Scenario: A KV bucket that exists but does not load as a key-value store fails the open
    Given a second client created the bus asset "KIT_KV" on the cloud test server as "work-queue retention"
    When the cloud bus opens on the cloud test server
    Then the open fails with code "unavailable"
    And the bus asset "KIT_KV" is still configured as "work-queue retention"
