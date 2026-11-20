@wp:WP-L1-03 @level:component @profile:embedded @profile:cloud @REQ-BUS-001
Feature: Publish on a subject and replay it by bus sequence
  Every message published on a subject gets a bus sequence. A subscriber
  replays a subject from the start or after a bus sequence it already has,
  then receives live messages, with no gap and no duplicate.

  @REQ-BUS-002 @id:BUS-002-01
  Scenario: Bus sequences on a subject increase while other subjects interleave
    When these messages are published in this order:
      | subject       | data |
      | run.r1.deltas | m1   |
      | run.r2.deltas | x1   |
      | run.r1.deltas | m2   |
      | run.r2.deltas | x2   |
      | run.r1.deltas | m3   |
    Then the bus sequences of "m1", "m2" and "m3" strictly increase
    And the bus sequences of "x1" and "x2" strictly increase

  @REQ-BUS-003 @id:BUS-003-01
  Scenario Outline: A replay continues right after the given bus sequence
    Given messages "m1" to "m5" are published on subject "run.r1.deltas"
    When a subscriber subscribes to "run.r1.deltas" after the bus sequence of "<cursor>"
    And message "m6" is published on subject "run.r1.deltas"
    Then the subscriber receives <received> in that order and nothing else

    Examples:
      | cursor | received                 |
      | m2     | "m3", "m4", "m5", "m6"   |
      | m5     | "m6"                     |

  @REQ-BUS-003 @id:BUS-003-02
  Scenario: A replay from the start delivers every retained message, then live ones
    Given messages "m1" to "m3" are published on subject "run.r1.deltas"
    When a subscriber subscribes to "run.r1.deltas" from the start
    And message "m4" is published on subject "run.r1.deltas"
    Then the subscriber receives "m1", "m2", "m3", "m4" in that order and nothing else

  @REQ-BUS-004 @id:BUS-004-01
  Scenario: A live subscriber receives only messages published after it subscribed
    Given messages "m1" and "m2" are published on subject "run.r1.deltas"
    When a subscriber subscribes live to "run.r1.deltas"
    And message "m3" is published on subject "run.r1.deltas"
    Then the subscriber receives "m3" and nothing else

  @REQ-BUS-005 @id:BUS-005-01
  Scenario: A subscriber receives only its exact subject
    Given a subscriber subscribes live to "run.r1.deltas"
    When these messages are published in this order:
      | subject          | data |
      | run.r2.deltas    | x1   |
      | run.r1.deltas.m2 | x2   |
      | run.r1           | x3   |
      | run.r1.deltas    | m1   |
    Then the subscriber receives "m1" and nothing else

  @REQ-BUS-006 @id:BUS-006-01
  Scenario: Two subscribers receive the same messages in the same order
    Given subscribers "s1" and "s2" subscribe live to "run.r1.events"
    When 4 concurrent publishers publish 5 messages each on subject "run.r1.events"
    Then s1 and s2 each receive the same 20 messages in the same order
    And that order is the order of their bus sequences

  @REQ-BUS-009 @id:BUS-009-01
  Scenario: A cancelled subscription ends and receives nothing more
    Given a subscriber subscribes live to "run.r1.deltas"
    When the subscriber cancels its subscription
    And message "m1" is published on subject "run.r1.deltas"
    Then the subscription has ended without delivering a message

  @REQ-BUS-010 @id:BUS-010-01
  Scenario: A subscriber that falls behind is ended and resumes without loss
    Given the subscription buffer holds 4 messages
    And subscribers "slow" and "fast" subscribe live to "run.r1.deltas"
    And slow reads nothing
    When messages "m1" to "m10" are published on subject "run.r1.deltas"
    Then all 10 publishes returned a bus sequence
    And fast receives "m1" to "m10" in that order
    And slow's subscription ends after delivering "m1" to "m4"
    And a resubscription of slow after the bus sequence of "m4" receives "m5" to "m10" in that order and nothing else
