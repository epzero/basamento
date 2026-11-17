# Config: configuration, profiles and `kit serve`

Status: **Planned** (WP-L1-05). Design: ADR-0001, ADR-0004 and
[`design.md`](design.md). This capability covers how `kit` reads its
configuration, validates it, chooses a profile and a port implementation by
URL, and how `kit serve` starts and stops. The behavior of the ports behind
those URLs belongs to `specs/sql`, `specs/blob`, `specs/bus` and
`specs/secrets`; authentication modes belong to `specs/auth`.

### REQ-CONFIG-001 — Precedence
The configuration loader shall take each `kit` setting from the highest of its sources: command-line flag, environment variable `KIT_<KEY>`, configuration file, default.

### REQ-CONFIG-002 — Environment variable names
The configuration loader shall map a setting key `A.b` to the environment variable `KIT_A_B`, uppercasing every letter.

### REQ-CONFIG-003 — Unset sources
Where a source does not set a key, the configuration loader shall take that key from the next lower source.

### REQ-CONFIG-004 — Default profile
The configuration loader shall default `profile` to `embedded` when no source sets it.

### REQ-CONFIG-005 — Validation before start-up
Before opening any port, `kit serve` shall validate the effective configuration and report every invalid setting at once.

### REQ-CONFIG-006 — Errors name the offending key
When a setting fails validation, the configuration loader shall name the offending key in the error.

### REQ-CONFIG-007 — Secrets stay out of errors
If a setting fails validation, then the configuration loader shall keep its value out of the error.

### REQ-CONFIG-008 — Report secrets are masked
When `kit serve` reports the effective configuration, it shall print each setting whose key contains `secret`, `password` or `token` as `***`.

### REQ-CONFIG-009 — Supported profiles
If the effective `profile` is not `embedded` or `cloud`, then the configuration loader shall reject it with code `invalid_argument`.

### REQ-CONFIG-010 — Required cloud settings
If `profile` is `cloud` and a port URL is empty, then the configuration loader shall reject the configuration with code `invalid_argument`.

### REQ-CONFIG-011 — Embedded port URLs
While `profile` is `embedded`, the configuration loader shall accept for a port URL that is set only the URL schemes `sqlite` for `sql.url`, `file` for `blob.url`, `nats-embedded` for `bus.url` and `localsecrets` for `secrets.url`.

### REQ-CONFIG-012 — Cloud port URLs
While `profile` is `cloud`, the configuration loader shall accept for a port URL that is set only the URL schemes `postgres` for `sql.url`, `s3`, `gs` and `azblob` for `blob.url`, `nats` for `bus.url` and `awskms`, `gcpkms` and `hashivault` for `secrets.url`.

### REQ-CONFIG-013 — URL scheme selects the implementation
The configuration loader shall select each port's implementation by the scheme of that port's URL.

### REQ-CONFIG-014 — Unknown scheme
If a port URL has a scheme that is not in the list for its port and profile, then the configuration loader shall reject it with code `invalid_argument`.

### REQ-CONFIG-015 — Embedded data directory
The configuration loader shall default `data.dir` to `./.kit/data` when no source sets it.

### REQ-CONFIG-016 — Loopback listener
The configuration loader shall default `listen.addr` to `127.0.0.1:8080` when no source sets it.

### REQ-CONFIG-017 — Port URL syntax
If a port URL cannot be parsed, then the configuration loader shall reject it with code `invalid_argument`.

### REQ-CONFIG-018 — Validate command
When `kit config validate` runs, the configuration loader shall print the effective configuration with its sources and exit with status 0 if it is valid.

### REQ-CONFIG-019 — Validate reports problems
If the configuration is invalid, then `kit config validate` shall print every error and exit with status 2.

### REQ-CONFIG-020 — Ready only when every port opened
When `kit serve` reports itself ready, every port shall have opened.

### REQ-CONFIG-021 — Failed port
If a port cannot open, then `kit serve` shall stop and name that port.

### REQ-CONFIG-022 — Refused listener
If the configured listener address cannot be bound, then `kit serve` shall exit with status 1.

### REQ-CONFIG-023 — Graceful stop
When `kit serve` receives a termination signal, it shall stop accepting requests, finish the requests in flight within `shutdown.timeout` and close every port.

### REQ-CONFIG-024 — Stop timeout
If the requests in flight outlast `shutdown.timeout`, then `kit serve` shall close the ports anyway and exit with status 1.

### REQ-CONFIG-025 — Local runtime
When the embedded profile starts and `runtime.url` is unset, `kit serve` shall use the `local` runtime.

### REQ-CONFIG-026 — Embedded Temporal
When `kit serve` selects the `temporal-embedded` runtime, it shall start the embedded Temporal server before reporting itself ready.

### REQ-CONFIG-027 — Smoke checks the embedded binary
`@smoke` scenarios shall pass against `kit serve --profile embedded` started in an empty directory with a loopback listener.
