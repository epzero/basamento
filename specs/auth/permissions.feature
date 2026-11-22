@planned @wp:WP-L8-02 @security @level:component
Feature: Permission checks

  @REQ-AUTH-004 @id:AUTH-004-01
  Scenario: A viewer cannot cancel a run
    Given principal "vic" has role "viewer" in project "p1"
    When vic cancels a run in project "p1"
    Then the action fails with code "permission_denied"
    And an audit event records the denied action by vic

  @planned @wp:WP-L8-02 @security @REQ-AUTH-006 @id:AUTH-006-01
  Scenario: An undeclared action is denied
    Given principal "ana" of tenant "acme" has role "admin" in project "p1"
    When ana attempts undeclared action "run.frobnicate" on a run in project "p1"
    Then the action fails with code "permission_denied"
    And an audit event records the denied action by ana

  @planned @wp:WP-L8-02 @security @REQ-AUTH-007 @id:AUTH-007-01
  Scenario: An RPC without a declaration is refused
    Given the API declares an RPC with no action
    When a request reaches that RPC
    Then the RPC is refused before any permission check
    And no audit event records a denial

  @planned @wp:WP-L8-02 @security @REQ-AUTH-008 @id:AUTH-008-01
  Scenario: A viewer may read but not write
    Given principal "vic" of tenant "acme" has role "viewer" in project "p1"
    When vic lists runs in project "p1"
    Then the list contains the runs of project "p1"
    When vic starts a run in project "p1"
    Then the action fails with code "permission_denied"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-008 @id:AUTH-008-02
  Scenario: A member may run the lifecycle and converse
    Given principal "mia" of tenant "acme" has role "member" in project "p1"
    When mia starts a run in project "p1"
    Then the run is started
    When mia cancels the run in project "p1"
    Then the run is cancelled
    When mia resolves a gate of the run in project "p1"
    Then the gate is resolved
    When mia sends a message to the conversation in project "p1"
    Then the message is accepted

  @planned @wp:WP-L8-02 @security @REQ-AUTH-008 @id:AUTH-008-03
  Scenario: An admin manages credential names but not values
    Given principal "ada" of tenant "acme" has role "admin" in project "p1"
    When ada lists credential names in project "p1"
    Then the list contains the credential names of project "p1"
    And no credential value is returned
    When ada grants role "viewer" to "vic" in project "p1"
    Then the binding is created
    When ada resolves credential "cred://key" in project "p1"
    Then the credential value is used in the step but not returned to ada

  @planned @wp:WP-L8-02 @security @REQ-AUTH-009 @id:AUTH-009-01
  Scenario: The resource comes from the caller and not the runs table
    Given principal "vic" of tenant "acme" has role "viewer" in project "p1"
    And the runs table holds no row for the target resource
    When vic attempts action "run.get" on resource "run:r9" in project "p1"
    Then the action is allowed for vic
    And the permission check read no row from the runs table

  @planned @wp:WP-L8-02 @security @REQ-AUTH-010 @id:AUTH-010-01
  Scenario: Per-project and tenant-wide bindings both apply to a project resource
    Given principal "ana" of tenant "acme" has role "viewer" in project "p1"
    And principal "ana" of tenant "acme" has tenant-wide role "member"
    When ana attempts action "run.cancel" on a run in project "p1"
    Then the action is allowed for ana

  @planned @wp:WP-L8-02 @security @REQ-AUTH-010 @id:AUTH-010-02
  Scenario: A resource with no project matches only tenant-wide bindings
    Given principal "ana" of tenant "acme" has role "admin" in project "p1"
    When ana attempts action "audit.read" on a resource with no project
    Then the action fails with code "permission_denied"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-011 @id:AUTH-011-01
  Scenario: Bindings are granted, listed and revoked through the SQL store
    Given the binding store uses a tenant-scoped SQL table on SQLite
    When ada grants role "viewer" to "vic" in project "p1" of tenant "acme"
    Then the binding of "vic" role "viewer" in project "p1" is stored for tenant "acme"
    When ada lists the bindings of "vic" in project "p1"
    Then the list contains exactly the role "viewer"
    When ada revokes role "viewer" from "vic" in project "p1"
    Then the binding of "vic" in project "p1" is absent

  @planned @wp:WP-L8-02 @security @REQ-AUTH-012 @id:AUTH-012-01
  Scenario: A principal cannot grant a role holding a permission it lacks
    Given principal "vic" of tenant "acme" has role "viewer" in project "p1"
    When vic grants role "admin" to "bob" in project "p1"
    Then the action fails with code "permission_denied"
    And no binding for "bob" is stored

  @planned @wp:WP-L8-02 @security @REQ-AUTH-012 @id:AUTH-012-02
  Scenario: A grant requires the distinct grant action
    Given principal "mia" of tenant "acme" has role "member" in project "p1"
    And the role "member" holds every permission of the role "viewer"
    When mia grants role "viewer" to "bob" in project "p1"
    Then the action fails with code "permission_denied"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-013 @id:AUTH-013-01
  Scenario: An unknown role is rejected
    Given principal "ada" of tenant "acme" has role "admin" in project "p1"
    When ada grants role "ghost" to "bob" in project "p1"
    Then the action fails with code "invalid_argument"
    And the rejection names neither "ghost" nor any permission

  @planned @wp:WP-L8-02 @security @REQ-AUTH-014 @id:AUTH-014-01
  Scenario: A malformed project id is rejected
    Given principal "ada" of tenant "acme" has role "admin" in project "p1"
    When ada grants role "viewer" to "bob" in project "../p2"
    Then the action fails with code "invalid_argument"
    And no binding for tenant "acme" is stored

  @planned @wp:WP-L8-02 @security @REQ-AUTH-015 @id:AUTH-015-01
  Scenario: A check with no binding is denied by default
    Given principal "bob" of tenant "acme" has no role in project "p1"
    When bob attempts action "run.get" on a run in project "p1"
    Then the action fails with code "permission_denied"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-016 @id:AUTH-016-01
  Scenario: A principal of another tenant is denied
    Given principal "eve" of tenant "globex" has role "admin" in project "p1"
    When a check for tenant "acme" asks eve to cancel a run in project "p1"
    Then the action fails with code "permission_denied"
    And an audit event records the denied action by eve

  @planned @wp:WP-L8-02 @security @REQ-AUTH-017 @id:AUTH-017-01
  Scenario: Permissions of applicable role bindings are unioned
    Given principal "ana" of tenant "acme" has role "viewer" in project "p1"
    And principal "ana" of tenant "acme" has role "member" in project "p1"
    When ana attempts action "run.cancel" on a run in project "p1"
    Then the action is allowed for ana

  @planned @wp:WP-L8-02 @security @REQ-AUTH-018 @id:AUTH-018-01
  Scenario: Scopes of a worker token narrow but never widen
    Given principal "bot" of tenant "acme" has role "member" in project "p1"
    And the principal "bot" carries scopes "session:s1"
    When bot attempts action "run.cancel" on resource "run:r1" in project "p1"
    Then the action is allowed for bot
    When bot attempts action "run.cancel" on resource "run:r2" in project "p1"
    Then the action fails with code "permission_denied"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-019 @id:AUTH-019-01
  Scenario: A denial reveals no role or permission detail
    Given principal "vic" of tenant "acme" has role "viewer" in project "p1"
    When vic cancels a run in project "p1"
    Then the action fails with code "permission_denied"
    And the denial message names neither "run.cancel" nor the role "admin"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-020 @id:AUTH-020-01
  Scenario: A binding store that cannot answer fails closed
    Given principal "ada" of tenant "acme" has role "admin" in project "p1"
    And the binding store cannot answer checks
    When ada attempts action "run.get" on a run in project "p1"
    Then the action fails with code "unavailable"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-021 @id:AUTH-021-01
  Scenario: A condition is type-checked when the binding is granted
    Given principal "ada" of tenant "acme" has role "admin" in project "p1"
    When ada grants role "viewer" to "vic" in project "p1" with condition "resource.unknown == 1"
    Then the action fails with code "invalid_argument"
    When ada grants role "viewer" to "vic" in project "p1" with condition "now() > 0"
    Then the action fails with code "invalid_argument"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-022 @id:AUTH-022-01
  Scenario: A condition that exceeds its cost limit does not grant
    Given principal "vic" of tenant "acme" has a binding in project "p1" whose condition exceeds the cost limit
    When vic attempts action "run.get" on a run in project "p1"
    Then the action fails with code "permission_denied"
    And an audit event records the denied action by vic

  @planned @wp:WP-L8-02 @security @REQ-AUTH-023 @id:AUTH-023-01
  Scenario: A condition that errors at check time denies
    Given principal "vic" of tenant "acme" has a binding in project "p1" whose condition errors at check time
    When vic attempts action "run.get" on a run in project "p1"
    Then the action fails with code "permission_denied"
    And an audit event records the denied action by vic with reason "condition-error"

  @planned @wp:WP-L8-02 @security @REQ-AUTH-024 @id:AUTH-024-01
  Scenario: A denial audit event carries only safe fields
    Given principal "vic" of tenant "acme" has a binding in project "p1" with condition "resource.owner == principal.id"
    When vic cancels a run "r1" in project "p1"
    Then an audit event records action "authz.deny" and outcome "denied" by vic
    And the audit event names resource "run:r1" with no attributes
    And the audit event holds no condition text and no token

  @planned @wp:WP-L8-02 @security @REQ-AUTH-025 @id:AUTH-025-01
  Scenario: A failed audit record does not turn a denial into an allow
    Given principal "vic" of tenant "acme" has role "viewer" in project "p1"
    And the audit store rejects every record
    When vic cancels a run in project "p1"
    Then the action fails with code "permission_denied"
    And the failed audit record is logged and counted

  @planned @wp:WP-L8-02 @security @REQ-AUTH-026 @id:AUTH-026-01
  Scenario: An allowed check is not audited
    Given principal "vic" of tenant "acme" has role "viewer" in project "p1"
    When vic lists runs in project "p1"
    Then the action is allowed for vic
    And no audit event records an allowed check by vic

  @planned @wp:WP-L8-02 @security @profile:cloud @REQ-AUTH-027 @id:AUTH-027-01
  Scenario: Row-level security hides other tenants on every tenant table
    Given the cloud profile uses a Postgres store with row-level security
    And the application role lacks BYPASSRLS and is not the table owner
    When a transaction sets the tenant to "acme" and reads the credentials, audit and authz tables
    Then the transaction sees only rows of tenant "acme"

  @planned @wp:WP-L8-02 @security @profile:cloud @REQ-AUTH-028 @id:AUTH-028-01
  Scenario: No tenant variable means no rows
    Given the cloud profile uses a Postgres store with row-level security
    When a transaction reads the audit table without a tenant variable
    Then the transaction sees no rows from the audit table

  @planned @wp:WP-L8-02 @security @profile:cloud @REQ-AUTH-029 @id:AUTH-029-01
  Scenario: Migrations create the row-level security policies
    Given the cloud profile opens a fresh Postgres store
    When the migrations finish
    Then the credentials, audit and authz tables each have a FORCE ROW LEVEL SECURITY policy
