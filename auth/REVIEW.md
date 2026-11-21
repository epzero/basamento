# Review guide: auth

High risk: a mistake here leaks data across tenants or grants access.

- Every repository call carries a tenant context; there is no query path that
  can omit it.
- Authorization is checked once, in the interceptor, before any side effect;
  denials return `permission_denied` and write an audit event.
- Token validation checks issuer keys, audience and expiry; clock skew
  allowances are explicit and small.
- Secrets and password hashes never appear in logs, errors or responses.
- Every auth mode refuses non-loopback listen addresses until request
  authentication is wired into kit serve; lifting that rule needs the
  authenticator in the serve path first.
