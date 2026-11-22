# Authorization: design notes for the implementer

Work package WP-L8-02 (EP0-206), owned directory `auth/authz`. Requirements:
[`requirements.md`](requirements.md) (REQ-AUTH-006 … REQ-AUTH-031). Scenarios:
[`permissions.feature`](permissions.feature). Decisions: [ADR-0008](../../docs/adr/0008-auth-modes-and-in-app-authorization.md)
(in-app RBAC, per-project role bindings in SQL, one check in a connect
interceptor, optional CEL conditions, denial audit, Postgres row-level security),
[ADR-0015](../../docs/adr/0015-how-the-pr-gate-runs-postgres.md) (the Postgres
lane of the gate), [`docs/architecture/auth.md` §2](../../docs/architecture/auth.md)
and [`specs/sql/design.md` §4](../sql/design.md) (RLS assigned to this WP).

**This file adds no behaviour.** It records the non-observable choices an
implementer must follow so the requirements stay about what a caller can see.
Where this file and the requirements disagree, the requirements win.

## 1. Action catalog

An **action** is a `<area>.<verb>` string, the same shape `platform.AuditEvent.Action`
already uses. The catalog is a declaration in Go, not a row in SQL: a map from
action string to the resource kind it acts on. `authz.Check` denies any action
not in the map (REQ-AUTH-006). The map below is the catalog for what exists now;
adding a built-in role or an action later is additive, never a renumbering or a
rename (contracts are additive).

| Action | Resource kind | Class | Smallest role |
| --- | --- | --- | --- |
| `run.list` | project | read | viewer |
| `run.get` | run | read | viewer |
| `invocation.get` | invocation | read | viewer |
| `invocation.events` | invocation | read | viewer |
| `stream.attach` | run | read | viewer |
| `stream.reattach` | run | read | viewer |
| `stream.detach` | run | read | viewer |
| `browser.session.read` | browser session | read | viewer |
| `pool.list` | tenant | read | viewer |
| `worker.list` | pool | read | viewer |
| `binding.list` | project | read | viewer |
| `run.start` | run | write | member |
| `run.cancel` | run | write | member |
| `gate.resolve` | gate | write | member |
| `conversation.send` | conversation | write | member |
| `invocation.start` | invocation | write | member |
| `invocation.cancel` | invocation | write | member |
| `browser.session.start` | browser session | write | member |
| `browser.session.cancel` | browser session | write | member |
| `pool.join` | pool | write | member |
| `pool.leave` | pool | write | member |
| `binding.grant` | project | write | admin |
| `binding.revoke` | project | write | admin |
| `credential.names` | project | read | admin |
| `credential.put` | credential name | write | admin |
| `credential.delete` | credential name | write | admin |
| `credential.resolve` | credential name | use | admin |
| `worker.remove` | worker | write | admin |
| `audit.read` | tenant | read | admin |

`credential.resolve` is the producer that audits credential use itself, with
action `credential.resolve` and outcome `allowed` or `failed` (the credential
store already does this); an authorization check that allows it writes no
second event (REQ-AUTH-026). No role can read a credential *value* back: the
`credential.names` action lists names only and `credential.resolve` returns the
value to the step, not to the caller.

**WP-L6-03 rule.** Every RPC declares, in its registration, its action and a
per-RPC resolver that builds the `Resource` from the request. The connect
interceptor reads that declaration; an RPC without one is refused at wiring
time and never reaches `Check` (REQ-AUTH-007). A caller never passes a bare
action string or a resource id to the interceptor for it to trust.

## 2. Built-in roles

Roles are declared in Go as fixed sets of actions from the catalog. A role is a
set of permissions; a permission *is* a declared action. There are three
built-in roles; custom roles are out of scope for this WP.

| Role | Permission set |
| --- | --- |
| `viewer` | Every `read`-class action above: `run.list`, `run.get`, `invocation.get`, `invocation.events`, `stream.attach`, `stream.reattach`, `stream.detach`, `browser.session.read`, `pool.list`, `worker.list`, `binding.list`. |
| `member` | All of `viewer`, plus `run.start`, `run.cancel`, `gate.resolve`, `conversation.send`, `invocation.start`, `invocation.cancel`, `browser.session.start`, `browser.session.cancel`, `pool.join`, `pool.leave`. Run lifecycle and converse, no binding or credential management. |
| `admin` | All of `member`, plus `binding.grant`, `binding.revoke`, `credential.names`, `credential.put`, `credential.delete`, `credential.resolve`, `worker.remove`, `audit.read`. Everything, including binding and credential *names*, never a credential value. |

The sets are supersets: `viewer ⊂ member ⊂ admin`. This is what makes the grant
authority rule (§4) usable.

## 3. Resource and binding model

A resource is the value the per-RPC resolver produces:

```go
type Resource struct {
    Project string            // may be empty
    Kind    string            // tenant-scoped kind from the catalog, e.g. "run"
    ID      string            // id within the tenant and kind, e.g. "r1"
    Attrs   map[string]any    // only for CEL conditions; never audited
    // Session is filled by the per-RPC resolver, the only party with the
    // domain store. It identifies the session a scoped worker token is bound
    // to. It is not audited and is not a CEL attribute.
    Session string            // optional; empty when the resource is not session-scoped
}
```

The caller supplies it; `auth/authz` never reads the runs table (REQ-AUTH-009).
This keeps the permission check free of the domain store and makes the same
check run at component and system level.

A **binding** names a principal, a role, optionally a project, and optionally a
condition. An empty `Project` means **tenant-wide**; a non-empty one scopes the
binding to that project. For a resource with a project, both its project's
bindings and the tenant-wide bindings are applicable; for a resource with no
project, only tenant-wide bindings are applicable (REQ-AUTH-010). A binding is
always tenant-scoped: its tenant comes from the context, never from the caller.

The store is one table on SQLite and Postgres through `platform/sql`:

```sql
CREATE TABLE authz_bindings (
  tenant    TEXT NOT NULL,
  project   TEXT NOT NULL,          -- '' = tenant-wide
  principal TEXT NOT NULL,
  role      TEXT NOT NULL,
  condition TEXT,                   -- nullable, validated at grant
  PRIMARY KEY (tenant, project, principal, role)
);
```

The Go API is `Grant(ctx, principal, role, project, condition)`,
`Revoke(ctx, principal, role, project)` and `List(ctx, principal, project)`.
Input grammars (REQ-AUTH-014): a project id is
`^[a-z0-9][a-z0-9_-]{0,63}$`; a principal id is
`^[A-Za-z0-9][A-Za-z0-9._@:/-]{0,127}$`; an empty project means tenant-wide and
is the only accepted empty id. A role outside the three names above is rejected
with `invalid_argument`, and the rejection names neither the role nor any
permission (REQ-AUTH-013).

## 4. Grant authority

Granting and revoking are actions of their own (`binding.grant`,
`binding.revoke`), so they pass through the same check as any other RPC. On top
of holding the action, a grantor must hold **every** permission in the role
being granted: because the sets are supersets, only `admin` can grant `admin`,
`member` or `viewer`, and `member` cannot grant `viewer` (it lacks
`binding.grant`) even though it holds all of `viewer`'s permissions
(REQ-AUTH-012). Revocation follows the same authority rule. `List` needs
`binding.list`.

## 5. Decision procedure

```
func Check(ctx, principal, action, resource) error {
    if !catalog.Has(action) {                       // REQ-AUTH-006
        return deny(noBinding)                       // generic denial, no detail
    }
    if TenantFrom(ctx) != principal.Tenant {         // REQ-AUTH-016
        return deny(tenantMismatch)
    }
    bindings, err := store.Bindings(ctx, principal.ID, resource.Project)
    if err != nil {                                  // REQ-AUTH-020
        return unavailable()                         // deny; store could not answer
    }
    perms := PermissionSet{}
    for _, b := range bindings {                     // REQ-AUTH-017
        if holds(b.Condition, principal, resource) { // true when no condition
            perms = perms.Union(roles[b.Role])
        }
    }
    perms = perms.Narrow(principal.Scopes)           // REQ-AUTH-018; never widens
    if !perms.Has(action) {                          // REQ-AUTH-015
        return deny(noBinding)
    }
    return nil                                       // allowed; no audit event
}
```

`deny` writes the audit event (§7) and returns `permission_denied` with the
fixed message `"permission denied"` — no action, role or permission name, and
no reason category in the message (REQ-AUTH-019). The reason category is only
in the audit event. `unavailable` also denies; any other internal failure
follows the same fail-closed path.

`Narrow` applies `principal.Scopes` to the role union (REQ-AUTH-018,
REQ-AUTH-030). Scopes only ever subtract permissions; a scoped worker token
(REQ-AUTH-005) starts from the role union and can only lose permissions. The
only recognized scope form today is `session:<id>`: a scope of that form keeps
a granted permission only when `Resource.Session == <id>`. A scope string in
any other form grants nothing. A non-empty scope list with no recognized form
denies every action with the reason category `scope-narrowed` (fail closed);
an empty scope list does not narrow at all. The resolver must populate
`Resource.Session` for every resource a worker token can address (REQ-AUTH-005);
`auth/authz` still never reads the runs table (REQ-AUTH-009). Other scope forms
(action lists, resource lists) may be added later, additively.

`principal.Scopes` holds only authorization scopes minted by the system for
scoped worker or service tokens (REQ-AUTH-005, REQ-AUTH-031). An identity
provider's OAuth `scope` claim is never copied into it. This keeps an ordinary
OIDC user unnarrowed by provider scopes that have no authorization meaning,
while a token whose scopes were all unrecognized would otherwise be denied
under the fail-closed rule above.

## 6. CEL conditions

A condition is an [expression](../glossary.md) in the `expr` engine. It is
compiled and type-checked once, with `Grant`, against this environment:

```
principal: { id: string, scopes: list<string> }
resource:  { project: string, kind: string, id: string, attrs: map<string, dyn> }
```

Custom resource attributes live under `resource.attrs`, so a grant condition
uses `resource.attrs.owner` (not `resource.owner`) to type-check (REQ-AUTH-021).

The result must be `bool`. A condition that fails to compile or type-check, or
that references a clock or random function (`now`, `random`, and the like), is
rejected with `invalid_argument` and the binding is not stored (REQ-AUTH-021).
The environment is closed: no other variables, no vendor calls, no clock and
no random (consistent with the `expr` rules used elsewhere).

At check time the compiled condition is evaluated with a **cost limit of
10,000 cost units** (the `expr` cost budget). A condition that exceeds the
budget does not grant (REQ-AUTH-022); an evaluation error (for example a type
error on an `attrs` value) also does not grant and is recorded as a denial with
the reason category `condition-error` (REQ-AUTH-023). A missing or false
condition is an ordinary `no-binding` denial. `resource.attrs` is available to
the expression only and is never written to an audit event.

## 7. Denial audit

Every `deny` writes one `platform.AuditEvent`:

| Field | Value |
| --- | --- |
| `Action` | `authz.deny` |
| `Outcome` | `denied` |
| `Principal` | `principal.ID`, never a token |
| `Resource` | `"<kind>:<id>"`, e.g. `run:r1` — no `project`, no `attrs` |
| `Reason` | one safe category from the list below |
| `Tenant` | the context tenant, from `core.TenantFrom` |

Safe reason categories: `no-binding`, `tenant-mismatch`, `condition-error`,
`condition-cost-exceeded`, `scope-narrowed`, `undeclared-action`. No condition
text, token, role or permission name is ever written (REQ-AUTH-024). Allowed
checks write no event; the producers of sensitive actions audit those themselves
(REQ-AUTH-026). If `Record` fails, the denial is unchanged — `Check` still
returns `permission_denied` — and the failure is logged and counted as a metric
(REQ-AUTH-025); a failed audit never turns a denial into an allow.

## 8. Row-level security (cloud profile)

This is the second line of defence behind the tenant filter every
`platform/sql` repository already applies ([`specs/sql/design.md` §4](../sql/design.md)).
It is enforced only in the cloud profile.

- **Connection identity.** Migrations create an owner role that owns the tenant
  tables and a separate application role used by the running process. The
  application role has no `BYPASSRLS` and is not the owner, so it cannot turn
  policies off.
- **Tenant variable.** Before each transaction, `platform/sql` calls
  `set_config('app.tenant', $1, true)` with the tenant from the context as a
  bound parameter — never string-concatenated. `true` scopes it to the
  transaction. `current_setting('app.tenant', true)` returns NULL when unset.
- **Policy shape**, for every tenant table:

  ```sql
  ALTER TABLE <table> ENABLE ROW LEVEL SECURITY;
  ALTER TABLE <table> FORCE ROW LEVEL SECURITY;
  CREATE POLICY tenant_isolation ON <table>
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
  ```

  With the variable unset the predicate is unknown for every row, so the
  transaction sees no rows (REQ-AUTH-028). `FORCE` is required because the
  application role would otherwise be subject to policies only as non-owner;
  being a non-owner without `BYPASSRLS` is also required (REQ-AUTH-027).
- **Tables.** Every tenant table, including the credential store's, the audit
  store's and the new `authz_bindings` table, gets the policy. Migrations create
  the roles and the policies (REQ-AUTH-029); SQLite has no RLS and is unaffected.
- **Gate proof.** The scenarios tagged `@profile:cloud` run on the Postgres lane
  of the gate as [ADR-0015](../../docs/adr/0015-how-the-pr-gate-runs-postgres.md)
  decides: a fresh database per scenario, a server from `BDD_POSTGRES_URL` or an
  embedded Postgres 16, and a `postgres-unavailable` skip only when no server is
  usable. The component driver opens the store through its public port and
  asserts, on both the audit and `authz_bindings` tables, that a transaction with
  one tenant set sees only that tenant's rows and a transaction with no tenant
  variable sees none. No test hook is added to production code.

## 9. Delivery split

The WP is delivered in two parts, recorded in
[`docs/roadmap/L8-auth.md`](../../docs/roadmap/L8-auth.md):

- **(a) Core** — action catalog, built-in roles, caller-supplied resource,
  binding store and Go API, grant authority, decision procedure, CEL conditions
  and denial audit (§1–§7).
- **(b) RLS** — the cloud-profile policies and their gate proof (§8).

WP-L6-03 needs only the check, the roles, the bindings and the audit; it does
not need RLS. Part (a) therefore unblocks the API surface and part (b) follows
it, so the API does not wait for the Postgres work.
