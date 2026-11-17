@planned @wp:WP-L1-05 @level:component
Feature: Validating the configuration
  A wrong setting is reported before anything opens, names the key that is
  wrong, and never echoes the value of a secret.

  @REQ-CONFIG-005 @id:CONFIG-005-01
  Scenario: Every invalid setting is reported at once
    Given a configuration file sets "profile" to "desktop" and "listen.addr" to "8080"
    And the environment sets "KIT_SQL_URL" to "sqlite://" with an unparseable URL
    When the configuration is validated
    Then validation fails with 3 errors
    And the errors name "profile", "listen.addr" and "sql.url"

  @REQ-CONFIG-006 @id:CONFIG-006-01
  Scenario: A bad profile names the profile key
    Given a configuration file sets "profile" to "desktop"
    When the configuration is validated
    Then validation fails with 1 error naming "profile"

  @REQ-CONFIG-006 @id:CONFIG-006-02
  Scenario: A malformed port URL names the port key
    Given the environment sets "KIT_SQL_URL" to "sqlite://" with an unparseable URL
    When the configuration is validated
    Then validation fails with 1 error naming "sql.url"

  @REQ-CONFIG-007 @security @id:CONFIG-007-01
  Scenario: An error keeps a secret value out
    Given the environment sets "KIT_BUS_URL" to "nats://ana:hunter2@nats.example.test:4222" in the cloud profile
    When the configuration is loaded
    Then the error names "bus.url"
    And the error text does not contain "hunter2"

  @REQ-CONFIG-017 @id:CONFIG-017-01
  Scenario: An unparseable port URL is rejected
    Given the environment sets "KIT_SQL_URL" to "sqlite://" with an unparseable URL
    When the configuration is loaded
    Then the configuration fails with code "invalid_argument"

  @REQ-CONFIG-009 @id:CONFIG-009-01
  Scenario Outline: Only the two profiles are accepted
    Given a configuration file sets "profile" to "<profile>"
    When the configuration is loaded
    Then the configuration fails with code "invalid_argument"

    Examples:
      | profile  |
      | desktop  |
      | Embedded |

  @REQ-CONFIG-010 @id:CONFIG-010-01
  Scenario Outline: The cloud profile needs every port URL
    Given the cloud profile omits "<key>"
    When the configuration is loaded
    Then the configuration fails with code "invalid_argument" naming "<key>"

    Examples:
      | key         |
      | sql.url     |
      | blob.url    |
      | bus.url     |
      | secrets.url |

  @REQ-CONFIG-014 @id:CONFIG-014-01
  Scenario Outline: A scheme outside the port's list is rejected
    Given a configuration file sets "<key>" to "<url>"
    When the configuration is loaded
    Then the configuration fails with code "invalid_argument" naming "<key>"

    Examples:
      | key         | url                                 |
      | sql.url     | postgres://db.example.test/kit      |
      | blob.url    | bbolt:///var/lib/kit/blobs          |
      | bus.url     | amqp://bus.example.test             |
      | secrets.url | vault://vault.example.test          |

  @REQ-CONFIG-008 @id:CONFIG-008-01
  Scenario: The report masks secret settings
    Given the cloud profile has its port URLs
    And the environment sets "auth.client_secret" to "s3cr3t-value"
    When the effective configuration is reported
    Then "auth.client_secret" is printed as "***"
    And the report shows "log.level" as "info" from "default"
    And the report contains no "s3cr3t-value"
