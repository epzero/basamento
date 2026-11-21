@wp:WP-L1-05 @level:system @profile:embedded
Feature: Inspecting the configuration with kit config validate
  `kit config validate` shows the effective configuration and where each
  setting came from, so a deployment can be checked before it starts.

  @REQ-CONFIG-018 @id:CONFIG-018-01
  Scenario: A valid configuration is printed with its sources
    Given a configuration file sets "log.level" to "debug"
    And the environment sets "KIT_LISTEN_ADDR" to "127.0.0.1:7000"
    When "kit config validate" runs
    Then it exits with status 0
    And it prints "listen.addr" as "127.0.0.1:7000" from "env"
    And it prints "log.level" as "debug" from "file"

  @REQ-CONFIG-019 @id:CONFIG-019-01
  Scenario: An invalid configuration is reported and fails
    Given a configuration file sets "profile" to "desktop" and "listen.addr" to "8080"
    When "kit config validate" runs
    Then it exits with status 2
    And it prints 2 errors naming "profile" and "listen.addr"
