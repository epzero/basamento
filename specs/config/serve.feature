@planned @wp:WP-L1-05 @profile:embedded
Feature: Starting and stopping kit serve
  `kit serve` opens every port, says it is ready only when all of them are
  open, says which port it could not open, and stops on a termination signal
  without dropping a request in flight.

  @REQ-CONFIG-016 @level:system @smoke @id:CONFIG-016-02
  Scenario: The server listens on loopback with the defaults
    Given an empty data directory and the port "127.0.0.1:8080" free
    When "kit serve --profile embedded" starts with no other setting
    Then it reports the listener "127.0.0.1:8080"
    And its health check on "127.0.0.1:8080" answers "200"

  @REQ-CONFIG-020 @level:system @smoke @id:CONFIG-020-01
  Scenario: The embedded server becomes ready
    Given an empty data directory and a free loopback listener
    When "kit serve" starts in it
    Then it reports ready
    And the ready line reports the "sql", "blob", "bus" and "secrets" implementations
    And it answers the health check on its listener with "200"

  @REQ-CONFIG-021 @level:system @id:CONFIG-021-01
  Scenario: A port that cannot open is named
    Given a data directory that cannot be written
    When "kit serve" starts
    Then it exits with status 1 and reports that port "sql" could not open
    And it opens no listener

  @REQ-CONFIG-005 @level:system @smoke @id:CONFIG-005-02
  Scenario: Nothing opens when the configuration is invalid
    Given the embedded profile with "desktop" as "profile"
    And a free loopback listener
    When "kit serve" starts
    Then it exits with status 1 and reports the 1 error naming "profile"
    And it opens no listener and creates nothing in the data directory

  @REQ-CONFIG-022 @level:system @id:CONFIG-022-01
  Scenario: A listener that cannot bind stops the start-up
    Given the embedded profile with the listener "127.0.0.1:8080"
    And another process already listens on "127.0.0.1:8080"
    When "kit serve" starts
    Then it exits with status 1
    And it reports that "listen.addr" could not be bound
    And it never reports ready

  @REQ-CONFIG-024 @level:component @id:CONFIG-024-01
  Scenario: A slow request delays the stop no longer than the timeout
    Given the clock is frozen at "2026-11-01T00:00:00Z"
    And the server is ready with "shutdown.timeout" set to "5s"
    And a request is in flight
    When the server receives a termination signal
    And 5 seconds pass
    Then every port is closed
    And the server exits with status 1

  @REQ-CONFIG-023 @level:component @id:CONFIG-023-01
  Scenario: A termination signal stops the server cleanly
    Given the clock is frozen at "2026-11-01T00:00:00Z"
    And the server is ready with "shutdown.timeout" set to "5s"
    And a request is in flight
    When the server receives a termination signal
    And the in-flight request completes before "shutdown.timeout" elapses
    Then the request receives its response
    And new requests are refused
    And every port is closed
    And the server exits with status 0
