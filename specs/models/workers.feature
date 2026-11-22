@planned @wp:WP-L3-07 @runtime:any @level:component
Feature: Workers pull invocations from their pool's queue
  A worker serves what it declared to the pool it joined. The platform keeps
  the queue: it decides the order, respects what each worker can take, and
  never lets a lost worker cause a second paid start.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"

  @REQ-MODEL-012 @security @id:MODEL-012-01
  Scenario: A worker never receives another tenant's work
    Given tenant "acme" has pool "acme-gpu" and tenant "globex" has pool "globex-gpu"
    And worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    And tenant "globex" queued 1 invocation of "sdxl" for pool "globex-gpu"
    When worker "w1" asks for work with 4 free units
    Then worker "w1" receives 0 invocations
    And 1 invocation waits in pool "globex-gpu"

  @REQ-MODEL-012 @id:MODEL-012-02
  Scenario: A worker receives only models it declared
    Given worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    And tenant "acme" queued 1 invocation of "sdxl" and 1 invocation of "ffmpeg-transcode" for pool "acme-gpu"
    When worker "w1" asks for work with 4 free units
    Then worker "w1" receives 1 invocation, of model "sdxl"

  @REQ-MODEL-013 @id:MODEL-013-01
  Scenario: A model stays in the catalog when its worker leaves
    Given worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    And the catalog lists "sdxl" with availability "online"
    When worker "w1" leaves
    Then the catalog lists "sdxl" with availability "offline"

  @REQ-MODEL-014 @cost @id:MODEL-014-01
  Scenario: An invocation nobody leases fails at its queue timeout
    Given pool "acme-gpu" serves model "sdxl" and has no present worker
    And a run invoked "sdxl" with a queue timeout of 10 minutes and 500 milli-points reserved
    When 11 minutes pass
    Then the node fails with code "unavailable"
    And the reservation of 500 milli-points is released

  @REQ-MODEL-015 @id:MODEL-015-01
  Scenario: Interactive work is leased before older batch work
    Given worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    And tenant "acme" queued invocation "b1" of "sdxl" with class "batch"
    And 1 minute later tenant "acme" queued invocation "i1" of "sdxl" with class "interactive"
    When worker "w1" asks for work with 1 free unit
    Then worker "w1" receives invocation "i1"

  @REQ-MODEL-015 @id:MODEL-015-02
  Scenario: Within a class the oldest invocation is leased first
    Given worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    And tenant "acme" queued invocation "n1" of "sdxl" with class "normal"
    And 1 minute later tenant "acme" queued invocation "n2" of "sdxl" with class "normal"
    When worker "w1" asks for work with 1 free unit
    Then worker "w1" receives invocation "n1"

  @REQ-MODEL-016 @id:MODEL-016-01
  Scenario: Batch work that has waited past the ageing period overtakes newer normal work
    Given pool "acme-gpu" has an ageing period of 30 minutes
    And worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    And tenant "acme" queued invocation "b1" of "sdxl" with class "batch"
    And 31 minutes later tenant "acme" queued invocation "n1" of "sdxl" with class "normal"
    When worker "w1" asks for work with 1 free unit
    Then worker "w1" receives invocation "b1"

  @REQ-MODEL-017 @id:MODEL-017-01
  Scenario: The shared pool serves the tenant with less work in hand first
    Given worker "w1" joined the shared pool declaring model "sdxl"
    And tenants "acme" and "globex" each have weight 1 in the shared pool
    And tenant "acme" has 3 invocations of "sdxl" leased and queued invocation "a4" of "sdxl"
    And 1 minute later tenant "globex" queued invocation "g1" of "sdxl"
    When worker "w1" asks for work with 1 free unit
    Then worker "w1" receives invocation "g1"

  @REQ-MODEL-018 @id:MODEL-018-01
  Scenario: A tenant at its concurrency limit gets nothing more from the shared pool
    Given worker "w1" joined the shared pool declaring model "sdxl"
    And tenant "acme" has a concurrency limit of 2 in the shared pool
    And tenant "acme" has 2 invocations of "sdxl" leased and 1 invocation of "sdxl" waiting
    When worker "w1" asks for work with 4 free units
    Then worker "w1" receives 0 invocations

  @REQ-MODEL-019 @id:MODEL-019-01
  Scenario: A worker is given only what fits its free units
    Given worker "w1" joined pool "acme-gpu" declaring model "video-xl" at 4 units and model "sdxl" at 1 unit
    And tenant "acme" queued invocation "v1" of "video-xl"
    And 1 minute later tenant "acme" queued invocation "s1" of "sdxl"
    When worker "w1" asks for work with 2 free units
    Then worker "w1" receives invocation "s1"
    And invocation "v1" still waits

  @REQ-MODEL-020 @id:MODEL-020-01
  Scenario Outline: A worker that is <state> is given no work
    Given worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    And tenant "acme" queued 1 invocation of "sdxl" for pool "acme-gpu"
    And worker "w1" is <state>
    When worker "w1" asks for work with 4 free units
    Then worker "w1" receives 0 invocations

    Examples:
      | state    |
      | draining |
      | paused   |

  @REQ-MODEL-021 @id:MODEL-021-01
  Scenario: Work that was never started goes to another worker
    Given workers "w1" and "w2" joined pool "acme-gpu" declaring model "cloud-video"
    And worker "w1" leased invocation "v1" of "cloud-video" and did not report starting it
    When the lease of worker "w1" expires
    And worker "w2" asks for work with 1 free unit
    Then worker "w2" receives invocation "v1" with the same client ref as worker "w1" did

  @REQ-MODEL-021 @id:MODEL-021-02
  Scenario: Started work of a restart-safe model goes to another worker
    Given workers "w1" and "w2" joined pool "acme-gpu" declaring restart-safe model "ffmpeg-transcode"
    And worker "w1" leased invocation "t1" of "ffmpeg-transcode" and reported starting it
    When the lease of worker "w1" expires
    And worker "w2" asks for work with 1 free unit
    Then worker "w2" receives invocation "t1"
    And followers of invocation "t1" receive a "reset" event

  @REQ-MODEL-022 @cost @id:MODEL-022-01
  Scenario: Started work of a lost worker is never started again
    Given workers "w1" and "w2" joined pool "acme-gpu" declaring model "cloud-video"
    And a run invoked "cloud-video" with 500 milli-points reserved
    And worker "w1" leased that invocation and reported starting it
    When the lease of worker "w1" expires and the grace period passes
    And worker "w2" asks for work with 1 free unit
    Then worker "w2" receives 0 invocations
    And the node fails with code "worker_lost"
    And the run is charged 500 milli-points and the reservation is closed

  @REQ-MODEL-023 @cost @id:MODEL-023-01
  Scenario: A worker that comes back within the grace period finishes its work
    Given worker "w1" joined pool "acme-gpu" declaring model "cloud-video"
    And worker "w1" leased invocation "v1" of "cloud-video" and reported starting it
    And the lease of worker "w1" expired, so invocation "v1" is in doubt
    When worker "w1" resumes invocation "v1" within the grace period
    And worker "w1" reports success for invocation "v1"
    Then invocation "v1" ends as "succeeded"
    And the provider of worker "w1" accepted 1 invocation

  @REQ-MODEL-024 @cost @id:MODEL-024-01
  Scenario: A report on an ended invocation is refused
    Given worker "w1" joined pool "acme-gpu" declaring model "cloud-video"
    And invocation "v1" of "cloud-video" ended as "failed" with code "worker_lost" while worker "w1" was away
    When worker "w1" reports success for invocation "v1"
    Then the report is refused with code "lease_lost"
    And invocation "v1" remains "failed" with code "worker_lost"

  @REQ-MODEL-025 @id:MODEL-025-01
  Scenario: An invocation whose third lease expires fails
    Given workers "w1", "w2" and "w3" joined pool "acme-gpu" declaring restart-safe model "ffmpeg-transcode"
    And a run invoked "ffmpeg-transcode"
    And the leases of workers "w1" and "w2" on that invocation expired in turn
    When the lease of worker "w3" on that invocation expires
    Then the node fails with code "worker_lost"
    And 0 invocations wait in pool "acme-gpu"

  @REQ-MODEL-026 @id:MODEL-026-01
  Scenario: Cancelling a run reaches the worker in its next heartbeat
    Given worker "w1" joined pool "acme-gpu" declaring model "cloud-video"
    And a run invoked "cloud-video" and worker "w1" leased and started the invocation
    When the run is cancelled
    And worker "w1" sends a heartbeat
    Then the heartbeat answer tells worker "w1" to cancel that invocation

  @REQ-MODEL-027 @cost @id:MODEL-027-01
  Scenario: A platform outage does not expire leases
    Given leases last 2 minutes
    And worker "w1" joined pool "acme-gpu" declaring model "cloud-video"
    And worker "w1" leased invocation "v1" of "cloud-video" and reported starting it
    When the platform is down for 10 minutes and starts again
    And worker "w1" sends a heartbeat 1 minute later
    Then invocation "v1" is still leased to worker "w1"

  @REQ-MODEL-028 @id:MODEL-028-01
  Scenario: A second worker cannot redefine a model its pool already serves
    Given worker "w1" joined pool "acme-gpu" declaring model "sdxl" at 1 unit
    When worker "w2" joins pool "acme-gpu" declaring model "sdxl" at 2 units
    Then the join is refused with code "conflict"

  @REQ-MODEL-029 @security @id:MODEL-029-01
  Scenario: A revoked pool token cannot join
    Given pool "acme-gpu" has a pool token that was revoked
    When a worker joins pool "acme-gpu" with that token
    Then the join is refused with code "unauthenticated"
    And an audit event records the refused join to pool "acme-gpu"

  @REQ-MODEL-030 @security @id:MODEL-030-01
  Scenario: A removed worker can do nothing more
    Given worker "w1" joined pool "acme-gpu" declaring model "sdxl"
    When an operator removes worker "w1"
    And worker "w1" asks for work with 4 free units
    Then the request fails with code "unauthenticated"

  @REQ-MODEL-031 @id:MODEL-031-01
  Scenario: A tenant's own pool is preferred over another provider of the same model
    Given tenant "acme" has pool "acme-llm" serving model "chat-large"
    And a cloud provider also serves model "chat-large"
    When a run of tenant "acme" invokes "chat-large"
    Then the invocation is queued for pool "acme-llm"
    And the cloud provider accepted 0 invocations

  @REQ-MODEL-032 @cost @id:MODEL-032-01
  Scenario: Without spill-over, queued work never moves to a paid provider
    Given tenant "acme" has pool "acme-llm" serving model "chat-large" with no present worker
    And a cloud provider also serves model "chat-large"
    And tenant "acme" has spill-over off
    And a run of tenant "acme" invoked "chat-large" with a queue timeout of 10 minutes
    When 11 minutes pass
    Then the node fails with code "unavailable"
    And the cloud provider accepted 0 invocations

  @REQ-MODEL-033 @cost @id:MODEL-033-01
  Scenario: With spill-over on, work that waited long enough moves to the other provider
    Given tenant "acme" has pool "acme-llm" serving model "chat-large" priced at 0 milli-points, with no present worker
    And a cloud provider also serves model "chat-large" at an estimated 300 milli-points
    And tenant "acme" has spill-over on after 2 minutes
    And a run of tenant "acme" invoked "chat-large"
    When 3 minutes pass
    Then 0 invocations wait in pool "acme-llm"
    And the cloud provider accepted 1 invocation with the same client ref
    And 300 milli-points were reserved before that start

  # Real transport proof: run through WorkerService with a real loopback listener,
  # fake clock, no sleeps and no vendor calls — not the in-process shortcut.
  @REQ-MODEL-089 @security @id:MODEL-089-01
  Scenario: A worker behind NAT is served over WorkerService
    Given a platform listening for workers on a loopback listener and worker "w1" has no inbound route
    And worker "w1" joined pool "acme-gpu" with its pool token, declaring model "sdxl"
    And tenant "acme" queued 1 invocation of "sdxl" for pool "acme-gpu"
    When worker "w1" dials out to WorkerService and asks for work with 4 free units
    Then worker "w1" receives 1 invocation, of model "sdxl"
    And the platform attempted 0 outbound dials to worker "w1"

  # Real transport proof: run through the kit serve listener with a real loopback
  # listener, fake clock, no sleeps and no vendor calls — not the in-process shortcut.
  @REQ-MODEL-090 @level:system @id:MODEL-090-01
  Scenario: kit serve composes the worker side
    Given kit serve started with pool "acme-gpu" declared and worker "w1" configured, listening for workers on a loopback listener
    When worker "w1" dials out to WorkerService and finishes invocation "v1" of "sdxl"
    Then pool "acme-gpu" is registered
    And the model.Invoker accounted invocation "v1"
    And remote "cloud-llm" serving "sdxl" is assigned so the router can choose between worker "w1" and remote "cloud-llm"
