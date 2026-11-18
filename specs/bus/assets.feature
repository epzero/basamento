@planned @wp:WP-L1-03 @level:component @profile:embedded @profile:cloud
Feature: Streams and buckets the bus can use
  The bus shares a JetStream cluster with other clients. It creates what it
  needs when it finds nothing, accepts what already matches its configuration,
  and refuses anything else without changing it.

  @REQ-BUS-030 @id:BUS-030-01
  Scenario: A matching JetStream setup the bus did not create is used as it is
    Given the JetStream assets match the bus configuration
    When the cloud bus opens on "nats://127.0.0.1:<port>"
    Then the open succeeds
    And message "m1" is published on subject "run.r1.deltas"
    And a subscriber to "run.r1.deltas" from the start receives "m1" and nothing else

  @REQ-BUS-030 @contract @id:BUS-030-02
  Scenario Outline: A stream or bucket the bus cannot use is refused and left unchanged
    Given the bus asset "<asset>" is configured as "<fault>"
    When the cloud bus opens on "nats://127.0.0.1:<port>"
    Then the open fails with code "invalid_argument" with detail "asset" set to "<asset>"
    And the bus asset "<asset>" is still configured as "<fault>"

    Examples:
      | asset   | fault            |
      | KIT_BUS | a mirror         |
      | KIT_BUS | a source         |
      | KIT_BUS | retention limits |
      | KIT_KV  | a mirror         |
      | KIT_KV  | a source         |
      | KIT_KV  | retention limits |
