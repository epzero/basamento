@planned @wp:WP-L3-08 @runtime:any @level:component
Feature: Workers started on demand
  A pool can hold worker templates. A scaler asks a launcher for workers when
  work waits, within limits, and the workers leave by themselves when idle.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And pool "acme-gpu" has worker template "sdxl-gpu" serving model "sdxl" with capacity 4, minimum 0, maximum 3 and a cold start of 2 minutes
    And template "sdxl-gpu" uses a simulated launcher

  @REQ-MODEL-034 @id:MODEL-034-01
  Scenario: A template's models are listed before any worker exists
    Given pool "acme-gpu" has no present worker
    Then the catalog lists "sdxl" with availability "on_demand" and a cold start of 2 minutes

  @REQ-MODEL-035 @cost @id:MODEL-035-01
  Scenario: Waiting work makes the scaler ask for enough workers
    Given pool "acme-gpu" has no present worker
    When tenant "acme" queues 6 invocations of "sdxl" for pool "acme-gpu"
    Then the launcher was asked for 2 workers of template "sdxl-gpu"

  @REQ-MODEL-035 @cost @id:MODEL-035-02
  Scenario: Pending launches are not asked for twice
    Given tenant "acme" queued 4 invocations of "sdxl" for pool "acme-gpu"
    And the launcher was asked for 1 worker of template "sdxl-gpu", which has not joined yet
    When tenant "acme" queues 2 more invocations of "sdxl" for pool "acme-gpu"
    Then the launcher was asked for 2 workers of template "sdxl-gpu" in total

  @REQ-MODEL-035 @cost @id:MODEL-035-03
  Scenario: Work that present workers can take launches nothing
    Given a worker of template "sdxl-gpu" is present with 4 free units
    When tenant "acme" queues 3 invocations of "sdxl" for pool "acme-gpu"
    Then the launcher was asked for 0 workers of template "sdxl-gpu"

  @REQ-MODEL-036 @cost @id:MODEL-036-01
  Scenario: The scaler never exceeds a template's maximum
    Given pool "acme-gpu" has no present worker
    When tenant "acme" queues 40 invocations of "sdxl" for pool "acme-gpu"
    Then the launcher was asked for 3 workers of template "sdxl-gpu"

  @REQ-MODEL-036 @cost @id:MODEL-036-02
  Scenario: The scaler never exceeds the deployment's cap
    Given the deployment's cap is 2 workers on demand
    And pool "acme-gpu" has no present worker
    When tenant "acme" queues 40 invocations of "sdxl" for pool "acme-gpu"
    Then the launcher was asked for 2 workers of template "sdxl-gpu"

  @REQ-MODEL-037 @cost @security @id:MODEL-037-01
  Scenario: One tenant's backlog counts only up to its concurrency limit
    Given the shared pool has worker template "shared-gpu" serving model "sdxl" with capacity 4, minimum 0, maximum 10 and a cold start of 2 minutes
    And template "shared-gpu" uses a simulated launcher
    And tenant "acme" has a concurrency limit of 4 in the shared pool
    When tenant "acme" queues 40 invocations of "sdxl" for the shared pool
    Then the launcher was asked for 1 worker of template "shared-gpu"

  @REQ-MODEL-038 @id:MODEL-038-01
  Scenario: The scaler keeps a template's minimum without any work
    Given pool "acme-gpu" has worker template "warm-gpu" serving model "sdxl-turbo" with capacity 4, minimum 1, maximum 3 and a cold start of 2 minutes
    And template "warm-gpu" uses a simulated launcher
    When the scaler runs with no invocation waiting
    Then the launcher was asked for 1 worker of template "warm-gpu"

  @REQ-MODEL-039 @security @id:MODEL-039-01
  Scenario: A launch join token admits one worker only
    Given the launcher was asked for 1 worker of template "sdxl-gpu" with join token "t1"
    And a worker joined pool "acme-gpu" with join token "t1"
    When another worker joins pool "acme-gpu" with join token "t1"
    Then the join is refused with code "unauthenticated"

  @REQ-MODEL-039 @security @id:MODEL-039-02
  Scenario: A launch join token expires at the cold-start deadline
    Given the launcher was asked for 1 worker of template "sdxl-gpu" with join token "t1" and a cold-start deadline of 4 minutes
    When 5 minutes pass
    And a worker joins pool "acme-gpu" with join token "t1"
    Then the join is refused with code "unauthenticated"

  @REQ-MODEL-040 @id:MODEL-040-01
  Scenario: A launched worker that differs from its template is refused
    Given the launcher was asked for 1 worker of template "sdxl-gpu" with join token "t1"
    When a worker joins pool "acme-gpu" with join token "t1" declaring model "sdxl" with capacity 8
    Then the join is refused with code "conflict"

  @REQ-MODEL-041 @id:MODEL-041-01
  Scenario: Three failed launches in a row degrade the template
    Given the simulated launcher accepts every request but no worker ever joins, with last error "quota exceeded"
    And a run invoked "sdxl" with a queue timeout of 30 minutes
    When 3 launches of template "sdxl-gpu" in a row pass their cold-start deadlines
    Then template "sdxl-gpu" is "degraded"
    And when the queue timeout passes, the node fails with code "unavailable" and message "quota exceeded"

  @REQ-MODEL-042 @cost @id:MODEL-042-01
  Scenario: An idle worker leaves by itself
    Given a worker of template "sdxl-gpu" is present with an idle-exit time of 5 minutes and no lease
    When 6 minutes pass
    Then that worker has left pool "acme-gpu"
    And the catalog lists "sdxl" with availability "on_demand" and a cold start of 2 minutes
