@planned @wp:WP-L1-05 @level:system @profile:embedded
Feature: Starting and stopping kit serve
  `kit serve` opens every port, says it is ready only when all of them are
  open, says which port it could not open, and stops on a termination signal
  without dropping a request in flight.

  @REQ-CONFIG-020 @smoke @id:CONFIG-020-01
  Scenario: The embedded server becomes ready
    Given an empty data directory and a free loopback listener
    When "kit serve" starts in it
    Then it reports ready
    And the sql, blob, bus and secrets ports are open when it reports ready
    And it answers the health check on its listener with "200"

  @REQ-CONFIG-021 @id:CONFIG-021-01
  Scenario: A port that cannot open is named
    Given the embedded profile with "sql://prod" for "sql.url"
    When "kit serve" starts
    Then it exits with status 1 and reports that port "sql" could not open
    And it opens no listener

  @REQ-CONFIG-005 @smoke @id:CONFIG-005-02
  Scenario: Nothing opens when the configuration is invalid
    Given the embedded profile with "desktop" as "profile"
    And a free loopback listener
    When "kit serve" starts
    Then it exits with status 1 and reports the 1 error naming "profile"
    And it opens no listener and no port

  @REQ-CONFIG-022 @id:CONFIG-022-01
  Scenario: A listener that cannot bind stops the start-up
    Given the embedded profile with the listener "127.0.0.1:8080"
    And another process already listens on "127.0.0.1:8080"
    When "kit serve" starts
    Then it exits with status 1
    And it reports that "listen.addr" could not be bound
    And it keeps port "sql" closed

  @REQ-CONFIG-024 @id:CONFIG-024-01
  Scenario: A slow request delays the stop no longer than the timeout
    Given "kit serve" is ready with "shutdown.timeout" set to "5s"
    And a request in flight that finishes at 10 seconds
    When "kit serve" receives SIGTERM
    Then it closes its ports at 5 seconds
    And it exits with status 1

  @REQ-CONFIG-023 @id:CONFIG-023-01
  Scenario: A termination signal stops the server cleanly
    Given "kit serve" is ready with "shutdown.timeout" set to "5s"
    And a request in flight that finishes at 1 second
    When "kit serve" receives SIGTERM
    Then it closes its sql port at 1 second
    And the request receives its response
    And new requests to "127.0.0.1:8080" are refused
    And it exits with status 0
