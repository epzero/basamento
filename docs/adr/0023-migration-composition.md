# ADR-0023 — One goose track from many packages' migration sets

**Status:** accepted · **Date:** 2026-11-22  
**Decides:** EP0-216; refines ADR-0022 D3 (Postgres-only fragments in the same
set) and fixes the composition rule ADR-0022 left open. Numbered 0023 because
0019–0022 are in flight on unmerged branches (EP0-187, EP0-186, EP0-205,
EP0-214).

## Context

Every package that owns tables ships a goose migration set numbered from
`00001` inside its own `fs.FS`:

| Package | Files on `main` | Exposes |
| --- | --- | --- |
| `platform/credentials` | `00001_credentials.sql` | `Migrations() fs.FS` |
| `stream/eventlog` | `00001_eventlog.sql`, `00002_relay_leases.sql` | `Migrations() fs.FS` |
| `auth/authz` | arrives with WP-L8-02 part (a), EP0-115 | `Migrations() fs.FS` |

Nothing composes them. `cmd/kit` opens one set — `cmd/kit/migrations/00001_baseline.sql`,
a `SELECT 1` that exists only because goose needs one file — and
`platform/sql.Open` takes whatever `fs.FS` it is handed. The BDD harness
currently bridges the gap by hand: `internal/bdd/steps/authz_migrations.go`
reads two files and republishes them as versions 1 and 2. That bridge does not
scale in either direction:

- **The same local number means two different migrations.** Credentials'
  `00001` and eventlog's `00001` are different SQL with the same version.
  Whichever set opens a database first records version 1; a later open with the
  other set sees "version 1 applied" and never creates the other package's
  tables. goose cannot detect it: the version, not the text, is the identity
  (and it does not checksum migrations).
- **Renumbering at the merge is positional.** Put a package earlier in the
  merge order, or add a migration to one that sits earlier in it, and every
  later package's numbers shift. A database that already applied the old
  numbers no longer matches the binary: at best it is refused, at worst it
  thinks it is up to date.

ADR-0022 D3 requires one `goose_db_version` table and one advisory lock for
the whole database, and puts Postgres-only `NNNNN_<name>.postgres.sql`
fragments in the same `fs.FS`, excluded by exact basename on SQLite (their
versions must be excluded from the version math too). It deliberately leaves
the multi-package rule, the bands and the merge to this ADR, before any cloud
rollout of row-level security (ADR-0022 D7).

SQLite and Postgres must both work from the same embedded files, and REQ-SQL-008
(`specs/sql/migrations.feature` SQL-008-01) says an older binary refuses a
newer database with `unsupported`.

## Decision

### D1 — One goose track per database; versions come from stable bands

A database has exactly one goose version track, one `goose_db_version` table
and one advisory lock. The version of a migration is

```
version_id = band * BandSize + local
```

with `sql.BandSize = 1_000_000`, `local` in `1 … BandSize-1`, and
`sql.MaxBand = 9999` — versions are decimal integers below 10¹⁰, fit
`int64`, and sort the way goose needs them to.

A band belongs to one package and is **allocated once, for the life of the
project**. Adding a migration appends `local+1` inside the band; it never
shifts a number in this band or any other. Band 0 is the composition root and
holds only the baseline at local 1, so **version 1 stays the baseline** on
every existing `cmd/kit` database.

`version_id` is the identity of a migration. Two files with the same
`version_id` are the same migration; the merge (D4) therefore refuses two sets
in one band before goose sees them.

### D2 — The band ledger

The allocation ledger lives in the `platform/sql` package doc and is updated in
the same change that adds a package's band. It reads as follows when this ADR
is accepted:

| Band | Owner | First `version_id` |
| ---: | --- | ---: |
| 0 | `cmd/kit` (composition root; baseline only, no tables) | 1 |
| 1 | `platform/credentials` | 1 000 001 |
| 2 | `stream/eventlog` | 2 000 001 |
| 3 | `auth/authz` | 3 000 001 |
| 4–9999 | unallocated; the next capability with tables takes the next free band | — |

Rules:

- **Allocation, never movement.** A new package claims the next free band in
  the same change that adds its `MigrationBand` constant; the ledger row and
  the constant are that change. Moving a package to another band, or reusing a
  band, needs a later ADR.
- **Foreign keys point down the bands.** A migration may reference tables of
  packages in a strictly lower band or of its own package. A reference to a
  higher band is a design error (the table may not exist yet on a fresh
  database); `go tool gate rls` (EP0-218) can grow a check for it later.
- **`platform/sql` stays dependency-free.** It never imports a capability;
  each owning package exports `MigrationBand int64` beside its
  `Migrations() fs.FS`, and the host or harness passes both to the merge.

### D3 — How a package ships migrations

- Files are flat, named `<local>_<slug>.sql`, with the Postgres-only form
  `<local>_<slug>.postgres.sql` (ADR-0022 D3). No subdirectories; no other
  files in the set.
- `local` is unique and within `1 … 999 999`. A set is append-only: a released
  migration is never edited, renamed, renumbered or deleted, and a number once
  used is never reused. A gap in a band means a migration is missing or was
  moved — both are forbidden.
- A migration decides its own SQL for both dialects, or ships a sibling
  `.postgres.sql` fragment for what SQLite cannot express.
- `Migrations()` returns the whole set; the package never opens or migrates a
  database itself, and never passes its own set to `sql.Open` in a host.

Editing a released migration is invisible to goose (it stores versions, not
checksums); the rule is enforced in review, and the version check in D6 makes
a *removal* fail loudly at open.

### D4 — What `platform/sql` offers: `MergeMigrations`

```go
// MigrationSet is one package's contribution to a database's version track.
type MigrationSet struct {
    Name string // diagnostics only, e.g. "credentials"
    Band int64  // from the ledger in D2
    FS   fs.FS  // flat, as D3 requires
}

// MergeMigrations returns one flat fs.FS whose file names carry the absolute
// version: <version_id>_<name>[.postgres].sql.
func MergeMigrations(sets ...MigrationSet) (fs.FS, error)
```

- It is pure and deterministic: it reads each set, maps `local` to
  `band*BandSize+local`, and republishes the files in memory (an `fstest.MapFS`
  or equivalent) with the `.postgres.sql` suffix preserved. `Open` keeps its
  `fs.FS` signature; composition happens in the host, not inside the port.
- It validates, before goose sees anything: bands within `0 … MaxBand`;
  no two sets in one band; `local` within range and unique inside the set;
  the filename shape of D3; no subdirectories. Errors are `*core.Error` with
  `internal` (a malformed composition is a broken binary) and a fixed message
  naming the set and band. Package names, not file bodies or SQL, appear in
  messages.
- It is called once per database URL; the result is reused by every open of
  that URL (D7).

### D5 — goose options: out-of-order on, Postgres-only files excluded on SQLite

`runMigrations` opens the provider with:

- **`goose.WithAllowOutofOrder(true)`**, always. A package adding `00003` to
  band 1 after band 3 has been applied is normal under D1; without the option
  goose refuses to run older unapplied migrations and the host cannot open a
  database it should upgrade. With it, goose applies missing older migrations
  first, then the new ones, recording each in `goose_db_version`. Consequence:
  a newly added migration sees the *current* schema, not its band's vintage —
  it must not assume higher bands have not run — and `Down` rolls back in
  applied order, not version order (rollback stays change control, as in
  ADR-0022 D7).
- **On SQLite, `goose.WithExcludeNames`** with every merged basename ending in
  `.postgres.sql`. `WithExcludeNames` matches exact basenames, so the list is
  collected from the merged set (post-rewrite names) and the *same* list
  filters the version math and the applied-set comparison of D6. On Postgres
  nothing is excluded. One table, one lock, one provider either way
  (ADR-0022 D3).

### D6 — The schema version check over the merged set

Before any write, and under the same lock(s) as today (twice on Postgres,
inside the SQLite file lock), `platform/sql` compares two sets computed for the
dialect in use:

- `binary`: the versions of the merged `.sql` files, minus the names excluded
  on SQLite in D5;
- `applied`: the `goose_db_version` rows with `is_applied` true.

1. `max(applied) > max(binary)` — the older-binary-on-newer-database case of
   REQ-SQL-008: `unsupported`, message unchanged
   (`sql: database schema newer than binary`), details `{version, max}`.
2. Otherwise, `applied ⊄ binary` — the database carries a migration this
   binary does not ship (a removed or renumbered migration, or a band moved):
   `unsupported`, message `sql: database schema has a migration this binary
   does not carry`, details `{version}` for the lowest such version.

Anything else opens and lets goose apply the difference. `SchemaVersion`
keeps reporting the newest applied version. ADR-0022 D3's exclusion rule is
what makes check 2 safe on SQLite: a Postgres-only version can never be
applied there, and the binary set does not contain it either.

### D7 — Hosts and the harness compose once

- **One database, one merged set.** The composition root builds it once per
  database URL and every `Open` of that URL uses it. Components receive the
  opened `*sql.Store`; a capability never opens the database with its own set.
  Two different sets on one database is the failure this ADR removes.
- **`cmd/kit`** joins the composition root rule for the embedded and the cloud
  profile alike, in the kit-wiring follow-up below. Until then kit opens only
  band 0, which is exactly what it does today.
- **BDD** replaces `combinedAuthMigrations` and every world's package-local
  set with the same merged set for `w.dbURL` the host would build, so the gate
  exercises the production composition path. The conformance sample sets under
  `internal/bdd/sqltest` stay raw: they are the SQL port's own test set, not a
  composition of capability migrations.
- `FilterMigrations` and `ExcludeMigrationVersion` keep working on absolute
  versions; their prefix match becomes `strconv.FormatInt(version, 10)+"_"`
  instead of the five-digit `%05d_` form, since merged versions have more
  digits. Covered by the implementation change, not by this ADR.

## Alternatives considered

| Alternative | Why it lost |
| --- | --- |
| One goose table per package | ADR-0022 D3 pins one table, one lock and one schema version; cross-package ordering for foreign keys would still need a rule, plus N providers. |
| Global flat numbering with a checked-in registry of file names | Inserting a migration in an earlier package still shifts later numbers — the bug this ADR exists for. |
| Timestamp versions (Flyway style) | Order across packages becomes admission order and clock, two packages can pick the same timestamp, and packages would embed wall-clock choices. |
| Hash-derived band per package | Collisions are possible, and the order would be arbitrary — no way to keep foreign keys pointing down the bands. |
| Renumber at the merge (today's BDD helper) | The premise of EP0-216: numbers move when a package's set changes, and a database that applied the old numbers no longer matches. |
| Out-of-order disabled | Normal package evolution (a new migration in a lower band) would make every up-to-date host refuse to open. |
| One package owns the whole schema | Packages own their tables and layers; a global schema package would import every capability and break layering. |

## Consequences

- `goose_db_version` versions become large (for example 2 000 002). No
  database in the wild carries a band version yet: `cmd/kit` has only ever
  applied version 1, which stays version 1. A long-lived hand-made database
  with ad hoc renumbered versions is refused with `unsupported` (check 2) —
  intended: recreate it, or migrate it deliberately under change control.
- REQ-SQL-005…009 and `specs/sql/migrations.feature` are unchanged; the
  conformance sample sets are raw sets and keep their small versions.
- EP0-115's `combinedAuthMigrations` is a temporary bridge; the follow-up
  below replaces it and it must not spread to another world.
- This is a schema-versioning change: the implementation PR is high risk and
  needs the owner's `approve` before a GitHub approval, like this ADR.
- Documentation moves with the implementation change: the band ledger and the
  merge/check rules go into `platform/sql/doc.go`, the merge into
  `specs/sql/design.md` §2, and `docs/architecture/overview.md` gets the host
  rule where it describes profiles and `kit serve`.

## Follow-up work

1. **EP0-221 — implement ADR-0023 in `platform/sql`** — `MigrationSet`,
   `MergeMigrations`, `BandSize`/`MaxBand`, the provider options of D5, the
   check of D6, the BDD replacement of D7, and specs: scenarios for two sets
   with the same local number, a migration added to an earlier band after a
   later one applied, SQLite ignoring `.postgres.sql` fragments, and a database
   carrying a version the binary does not ship. `specs-needed`; needs a spec
   review and a work package before code.
2. **EP0-220 — `cmd/kit` cloud wiring** — compose the capability sets in
   `openSQL` with the same ledger bands, beside `sql.migrate_url` (EP0-217) and
   the RLS `Open` mode; cover it with a `kit serve` scenario. Blocked by
   EP0-221, and by EP0-217 for the config half.

Both are filed and linked from EP0-216; EP0-221 is with the designer
(`specs-needed`), EP0-220 waits on it.
