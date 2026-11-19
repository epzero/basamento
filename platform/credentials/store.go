package credentials

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// Backend keeps encrypted credential rows for the tenant in ctx. It never
// receives or returns a plaintext value: the store encrypts before Upsert and
// decrypts after Get (REQ-CRED-021).
type Backend interface {
	// Upsert inserts r, or replaces the ciphertext and updated_at of an
	// existing (tenant, name) row while keeping its created_at.
	Upsert(ctx context.Context, r Record) error
	// Get returns the row for name, or code not_found when the tenant has none.
	Get(ctx context.Context, name string) (Record, error)
	// Delete removes the tenant's row for name, or fails with code not_found.
	Delete(ctx context.Context, name string) error
	// List returns every row of the tenant in any order.
	List(ctx context.Context) ([]Record, error)
}

// Record is one encrypted credential row.
type Record struct {
	Name       string
	Ciphertext []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Option configures a Store.
type Option func(*options)

type options struct {
	logger *slog.Logger
}

// WithLogger directs operational log records from the store. When nil, each
// call site uses slog.Default() at that moment.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

func (o *options) loggerOrDefault() *slog.Logger {
	if o != nil && o.logger != nil {
		return o.logger
	}
	return slog.Default()
}

// Store implements platform.Credentials over a Backend, the Secrets port, an
// Audit sink and a clock. The composition root supplies non-nil dependencies;
// the store opens no database and parses no URL itself.
type Store struct {
	backend Backend
	audit   platform.Audit
	secrets platform.Secrets
	clock   core.Clock
	opts    options
}

var _ platform.Credentials = (*Store)(nil)

// New returns a store. Every operation takes its tenant from the context with
// core.TenantFrom and fails with code invalid_argument when there is none.
func New(backend Backend, audit platform.Audit, sec platform.Secrets, clock core.Clock, opts ...Option) *Store {
	var o options
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return &Store{backend: backend, audit: audit, secrets: sec, clock: clock, opts: o}
}

// Put stores or rotates value under name for the tenant of ctx. The value is
// encrypted before it reaches the backend; an existing name keeps its
// created_at and gets the new ciphertext and updated_at. It records no audit
// event (REQ-CRED-021, REQ-CRED-002).
func (s *Store) Put(ctx context.Context, name string, value []byte) error {
	if _, err := requireTenant(ctx); err != nil {
		return err
	}
	if !validName(name) {
		return errInvalidName()
	}
	ciphertext, err := s.secrets.Encrypt(ctx, value)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.backend.Upsert(ctx, Record{
		Name:       name,
		Ciphertext: ciphertext,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// Resolve returns the value for ref, "cred://<name>", for the tenant of ctx.
// It validates the tenant and the reference before touching storage, reads the
// row, decrypts it, then records exactly one audit event. A malformed reference
// or a missing tenant is rejected without an audit event (REQ-CRED-032). If the
// audit event cannot be written, Resolve returns no value and fails with code
// unavailable (REQ-CRED-028).
func (s *Store) Resolve(ctx context.Context, ref string) ([]byte, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	name, ok := referenceName(ref)
	if !ok {
		return nil, errInvalidReference()
	}
	record, err := s.backend.Get(ctx, name)
	if err != nil {
		return nil, s.fail(ctx, tenant, ref, credentialError(err))
	}
	value, err := s.secrets.Decrypt(ctx, record.Ciphertext)
	if err != nil {
		if core.CodeOf(err) == core.CodeInvalidArgument {
			return nil, s.fail(ctx, tenant, ref, errUndecryptable())
		}
		return nil, s.fail(ctx, tenant, ref, err)
	}
	if err := s.record(ctx, tenant, ref, "allowed", ""); err != nil {
		return nil, err
	}
	return value, nil
}

// Delete removes name for the tenant of ctx. A name the tenant does not store
// fails with code not_found, the same code and message as another tenant's
// credential (REQ-CRED-004, REQ-CRED-011). It records no audit event.
func (s *Store) Delete(ctx context.Context, name string) error {
	if _, err := requireTenant(ctx); err != nil {
		return err
	}
	if !validName(name) {
		return errInvalidName()
	}
	if err := s.backend.Delete(ctx, name); err != nil {
		return credentialError(err)
	}
	return nil
}

// Names returns the tenant's credential names sorted by the bytes of their
// UTF-8 names, never a value (REQ-CRED-012, REQ-CRED-015, REQ-CRED-016).
func (s *Store) Names(ctx context.Context) ([]string, error) {
	if _, err := requireTenant(ctx); err != nil {
		return nil, err
	}
	records, err := s.backend.List(ctx)
	if err != nil {
		return nil, credentialError(err)
	}
	names := make([]string, 0, len(records))
	for _, r := range records {
		names = append(names, r.Name)
	}
	slices.Sort(names)
	return names, nil
}

// fail audits a failed resolution and then returns the failure. An audit write
// failure replaces the failure with unavailable and no value is returned.
func (s *Store) fail(ctx context.Context, tenant core.TenantID, ref string, cause error) error {
	if err := s.record(ctx, tenant, ref, "failed", string(core.CodeOf(cause))); err != nil {
		return err
	}
	return cause
}

func (s *Store) record(ctx context.Context, tenant core.TenantID, ref, outcome, reason string) error {
	u := UseFrom(ctx)
	event := platform.AuditEvent{
		Time:      s.clock.Now().UTC(),
		Tenant:    tenant,
		Principal: u.Principal,
		Action:    "credential.resolve",
		Resource:  ref,
		Outcome:   outcome,
		RunID:     u.RunID,
		Step:      u.StepID,
		Reason:    reason,
	}
	if err := s.audit.Record(ctx, event); err != nil {
		s.opts.loggerOrDefault().ErrorContext(ctx, "credential audit write failed",
			slog.String("action", event.Action),
			slog.String("outcome", event.Outcome),
			slog.String("error", err.Error()))
		return &core.Error{Code: core.CodeUnavailable, Message: "audit store unavailable", Retryable: true}
	}
	return nil
}

func requireTenant(ctx context.Context) (core.TenantID, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "tenant is required", nil)
	}
	return tenant, nil
}

// credentialError maps a backend error to the store's fixed outcome: a missing
// or another tenant's row is not_found with one message, so a caller cannot
// tell the two apart (REQ-CRED-010, REQ-CRED-011).
func credentialError(err error) error {
	if core.CodeOf(err) == core.CodeNotFound {
		return errNotFound()
	}
	return err
}

func errInvalidName() error {
	return core.NewError(core.CodeInvalidArgument, "invalid credential name", nil)
}

func errInvalidReference() error {
	return core.NewError(core.CodeInvalidArgument, "invalid credential reference", nil)
}

func errNotFound() error {
	return core.NewError(core.CodeNotFound, "credential not found", nil)
}

func errUndecryptable() error {
	return core.NewError(core.CodeInternal, "stored credential cannot be decrypted", nil)
}
