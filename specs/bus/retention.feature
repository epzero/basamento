@wp:WP-L1-03 @level:component @profile:embedded @profile:cloud @REQ-BUS-001
Feature: Purging a subject
  The bus keeps a subject's messages until the subject is purged; the
  streaming layer purges the deltas of a message shortly after it completes.
  A replay never silently skips purged messages.

  @REQ-BUS-007 @id:BUS-007-01
  Scenario: Purging a subject drops its messages and no other subject's
    Given messages "m1" to "m3" are published on subject "run.r1.msg.a"
    And message "x1" is published on subject "run.r1.msg.b"
    When subject "run.r1.msg.a" is purged
    Then a subscriber from the start of "run.r1.msg.a" receives nothing
    And a subscriber from the start of "run.r1.msg.b" receives "x1" and nothing else

  @REQ-BUS-007 @id:BUS-007-02
  Scenario: Bus sequences keep increasing after a purge
    Given messages "m1" to "m3" are published on subject "run.r1.msg.a"
    And subject "run.r1.msg.a" is purged
    When message "m4" is published on subject "run.r1.msg.a"
    Then the bus sequence of "m4" is greater than the bus sequence of "m3"
    And a subscriber from the start of "run.r1.msg.a" receives "m4" and nothing else

  @REQ-BUS-008 @id:BUS-008-01
  Scenario Outline: A replay from before the purge is refused
    Given messages "m1" to "m3" are published on subject "run.r1.msg.a"
    And subject "run.r1.msg.a" is purged
    When a subscriber subscribes to "run.r1.msg.a" after the bus sequence of "<cursor>"
    Then the subscription fails with code "not_found"
    And the error reports the bus sequence of "m3" as the last purged

    Examples:
      | cursor |
      | m1     |
      | m2     |

  @REQ-BUS-008 @id:BUS-008-02
  Scenario: A replay from the last purged bus sequence is accepted
    Given messages "m1" to "m3" are published on subject "run.r1.msg.a"
    And subject "run.r1.msg.a" is purged
    When a subscriber subscribes to "run.r1.msg.a" after the bus sequence of "m3"
    And message "m4" is published on subject "run.r1.msg.a"
    Then the subscriber receives "m4" and nothing else
