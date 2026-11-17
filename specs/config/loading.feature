@wp:WP-L1-05 @level:component
Feature: Loading the configuration
  Every `kit` process builds one effective configuration from command-line
  flags, environment variables, a configuration file and the built-in
  defaults, from the highest source down, and can show where each setting
  came from.

  @REQ-CONFIG-001 @id:CONFIG-001-01
  Scenario: The highest source wins for every setting
    Given a configuration file sets "listen.addr" to "127.0.0.1:9000" and "log.level" to "debug"
    And the environment sets "KIT_LISTEN_ADDR" to "127.0.0.1:7000"
    And a "kit serve" flag sets "listen.addr" to "127.0.0.1:6000"
    When the configuration is loaded
    Then "listen.addr" is "127.0.0.1:6000" from "flag"
    And "log.level" is "debug" from "file"

  @REQ-CONFIG-001 @id:CONFIG-001-02
  Scenario: The environment variable wins over the configuration file
    Given a configuration file sets "log.level" to "debug"
    And the environment sets "KIT_LOG_LEVEL" to "error"
    When the configuration is loaded
    Then "log.level" is "error" from "env"

  @REQ-CONFIG-002 @id:CONFIG-002-01
  Scenario: A setting key maps to its environment variable name
    Given the environment sets "KIT_BUS_URL" to "nats-embedded:///data/nats"
    When the configuration is loaded
    Then "bus.url" is "nats-embedded:///data/nats" from "env"

  @REQ-CONFIG-003 @id:CONFIG-003-01
  Scenario: A setting missing from the higher sources comes from the file
    Given a configuration file sets "log.format" to "json"
    When the configuration is loaded
    Then "log.format" is "json" from "file"

  @REQ-CONFIG-003 @id:CONFIG-003-02
  Scenario: A setting no source sets comes from the default
    Given a configuration file sets "log.level" to "debug"
    When the configuration is loaded
    Then "data.dir" is "./.kit/data" from "default"

  @REQ-CONFIG-016 @id:CONFIG-016-01
  Scenario: The listener is on loopback by default
    When the configuration is loaded
    Then "listen.addr" is "127.0.0.1:8080" from "default"

  @REQ-CONFIG-004 @id:CONFIG-004-01
  Scenario: The default profile is embedded
    Given a configuration file sets "log.level" to "debug"
    When the configuration is loaded
    Then "profile" is "embedded" from "default"

  @REQ-CONFIG-004 @id:CONFIG-004-02
  Scenario: The profile comes from a setting
    Given the environment sets "KIT_PROFILE" to "cloud"
    When the configuration is loaded
    Then "profile" is "cloud" from "env"

  @REQ-CONFIG-015 @id:CONFIG-015-01
  Scenario: The data directory comes from a setting
    Given a configuration file sets "data.dir" to "/srv/kit"
    When the configuration is loaded
    Then "data.dir" is "/srv/kit" from "file"

  @REQ-CONFIG-016 @wp:WP-L1-05 @level:system @smoke @id:CONFIG-016-02
  Scenario: The server listens on loopback with the defaults
    Given an empty data directory and the port "127.0.0.1:8080" free
    When "kit serve --profile embedded" starts with no other setting
    Then it reports the listener "127.0.0.1:8080"
    And its health check on "127.0.0.1:8080" answers "200"
