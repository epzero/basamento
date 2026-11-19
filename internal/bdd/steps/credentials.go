package steps

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/credentials"
	"github.com/epzero/basamento/platform/secrets"
	psql "github.com/epzero/basamento/platform/sql"
)

type credentialsWorld struct {
	dir    string
	dbURL  string
	db     *psql.Store
	audit  *countingAudit
	audits *credentials.AuditStore
	sec    *bddValueSecrets
	store  *credentials.Store
	back   credentials.Backend
	ext    *simExternal

	lastErr    error
	lastValue  []byte
	resolveErr error
	putErr     error
	deleteErr  error
	listErr    error
	auditErr   error
	lastOp     string
	listing    []string
	results    map[string]resolution
	waiting    *waitingResolution
}

type resolution struct {
	value []byte
	err   error
}

type waitingResolution struct {
	done   chan resolution
	result resolution
}

func newCredentialsWorld() *credentialsWorld {
	return &credentialsWorld{results: map[string]resolution{}}
}

func (w *credentialsWorld) teardown() {
	if w.db != nil {
		_ = w.db.Close()
		w.db = nil
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
		w.dir = ""
	}
}

// countingAudit passes every event to the SQL audit store and counts the
// calls, so a scenario can assert that no event reached the port.
type countingAudit struct {
	mu     sync.Mutex
	inner  platform.Audit
	calls  int
	reject bool
}

func (a *countingAudit) Record(ctx context.Context, e platform.AuditEvent) error {
	a.mu.Lock()
	a.calls++
	reject := a.reject
	a.mu.Unlock()
	if reject {
		return &core.Error{Code: core.CodeUnavailable, Message: "audit store unavailable", Retryable: true}
	}
	return a.inner.Record(ctx, e)
}

func (a *countingAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// bddValueSecrets is the simulated key service for value encryption, reusing
// the secrets steps' registry so "the key service is unreachable" applies.
type bddValueSecrets struct {
	reg  *simKeyRegistry
	key  string
	gate *secretsGate
}

type secretsGate struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *secretsGate) wait() {
	g.once.Do(func() { close(g.reached) })
	<-g.release
}

func (s *bddValueSecrets) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	if err := s.reg.checkAvailable(true, "__value__"); err != nil {
		return nil, err
	}
	return s.seal(plaintext)
}

func (s *bddValueSecrets) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	if s.gate != nil {
		s.gate.wait()
	}
	if err := s.reg.checkAvailable(false, "__value__"); err != nil {
		return nil, err
	}
	return s.open(ciphertext)
}

func (s *bddValueSecrets) seal(plaintext []byte) ([]byte, error) {
	inner := secrets.FrameValuePlaintext(plaintext)
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := s.reg.nextNonce(gcm.NonceSize())
	return gcm.Seal(nonce, nonce, inner, nil), nil
}

func (s *bddValueSecrets) open(ciphertext []byte) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, core.NewError(core.CodeInvalidArgument, "value cannot be decrypted", nil)
	}
	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	inner, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "value cannot be decrypted", nil)
	}
	return secrets.ParseValuePlaintext(inner)
}

func (s *bddValueSecrets) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.reg.keyBytes(s.key))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// simExternal is a platform.Backend that keeps encrypted records outside SQL.
type simExternal struct {
	mu      sync.Mutex
	records map[string]map[string]credentials.Record
}

func newSimExternal() *simExternal {
	return &simExternal{records: map[string]map[string]credentials.Record{}}
}

func (e *simExternal) Upsert(ctx context.Context, r credentials.Record) error {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return core.NewError(core.CodeInvalidArgument, "tenant is required", nil)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.records[string(tenant)] == nil {
		e.records[string(tenant)] = map[string]credentials.Record{}
	}
	if old, exists := e.records[string(tenant)][r.Name]; exists {
		r.CreatedAt = old.CreatedAt
	}
	e.records[string(tenant)][r.Name] = r
	return nil
}

func (e *simExternal) Get(ctx context.Context, name string) (credentials.Record, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return credentials.Record{}, core.NewError(core.CodeInvalidArgument, "tenant is required", nil)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r, found := e.records[string(tenant)][name]
	if !found {
		return credentials.Record{}, core.NewError(core.CodeNotFound, "credential not found", nil)
	}
	return r, nil
}

func (e *simExternal) Delete(ctx context.Context, name string) error {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return core.NewError(core.CodeInvalidArgument, "tenant is required", nil)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, found := e.records[string(tenant)][name]; !found {
		return core.NewError(core.CodeNotFound, "credential not found", nil)
	}
	delete(e.records[string(tenant)], name)
	return nil
}

func (e *simExternal) List(ctx context.Context) ([]credentials.Record, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, core.NewError(core.CodeInvalidArgument, "tenant is required", nil)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]credentials.Record, 0, len(e.records[string(tenant)]))
	for _, r := range e.records[string(tenant)] {
		out = append(out, r)
	}
	return out, nil
}

func (e *simExternal) count(tenant string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.records[tenant])
}

func (e *simExternal) ciphertext(tenant, name string) ([]byte, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, found := e.records[tenant][name]
	return r.Ciphertext, found
}

func (e *simExternal) containsPlaintext(value string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, byName := range e.records {
		for _, r := range byName {
			if bytes.Contains(r.Ciphertext, []byte(value)) {
				return true
			}
		}
	}
	return false
}

func credentialsW(ctx context.Context) *credentialsWorld {
	w := world(ctx)
	if w.Credentials == nil {
		w.Credentials = newCredentialsWorld()
	}
	return w.Credentials
}

func openCredentialsStore(ctx context.Context, mk string, external bool) error {
	w := credentialsW(ctx)
	sw := secretsW(ctx)
	sw.sim.hold(mk)
	if w.db == nil {
		dir, err := os.MkdirTemp("", "cred-*")
		if err != nil {
			return err
		}
		w.dir = dir
		w.dbURL = "sqlite:///" + filepath.Join(dir, "credentials.db")
		db, err := psql.Open(ctx, w.dbURL, credentials.Migrations())
		if err != nil {
			_ = os.RemoveAll(dir)
			w.dir = ""
			return err
		}
		w.db = db
	}
	w.audits = credentials.NewSQLAudit(w.db)
	w.audit = &countingAudit{inner: w.audits}
	if external {
		w.ext = newSimExternal()
		w.back = w.ext
	} else {
		w.back = credentials.NewSQLBackend(w.db)
	}
	w.sec = &bddValueSecrets{reg: sw.sim, key: mk}
	w.store = credentials.New(w.back, w.audit, w.sec, world(ctx).Core.clock,
		credentials.WithLogger(slog.New(sw.logHandler)))
	w.results = map[string]resolution{}
	return nil
}

func reopenCredentialsStore(ctx context.Context, mk string) error {
	w := credentialsW(ctx)
	sw := secretsW(ctx)
	sw.sim.hold(mk)
	w.audits = credentials.NewSQLAudit(w.db)
	w.audit = &countingAudit{inner: w.audits}
	w.store = credentials.New(credentials.NewSQLBackend(w.db), w.audit, w.sec, world(ctx).Core.clock,
		credentials.WithLogger(slog.New(sw.logHandler)))
	return nil
}

func credentialsCtx(ctx context.Context, tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func credentialsUseCtx(ctx context.Context, tenant, principal, run, step string) context.Context {
	c := credentialsCtx(ctx, tenant)
	return credentials.WithUse(c, credentials.Use{Principal: principal, RunID: run, StepID: step})
}

func recordResolution(ctx context.Context, tenant string, value []byte, err error) {
	w := credentialsW(ctx)
	w.lastValue = value
	w.resolveErr = err
	w.lastOp = "resolve"
	setCredentialsError(ctx, err)
	w.results[tenant] = resolution{value: value, err: err}
}

// setCredentialsError keeps the shared steps that inspect another domain's
// last error (resolution code, error does not contain, no value returned) in
// step with this domain's most recent operation.
func setCredentialsError(ctx context.Context, err error) {
	w := credentialsW(ctx)
	w.lastErr = err
	world(ctx).Prompt.lastErr = err
	secretsW(ctx).lastErr = err
}

func credentialTableRows(table *godog.Table) ([]map[string]string, error) {
	if len(table.Rows) == 0 {
		return nil, fmt.Errorf("empty table")
	}
	header := table.Rows[0]
	keys := make([]string, len(header.Cells))
	for i, cell := range header.Cells {
		keys[i] = cell.Value
	}
	var out []map[string]string
	for _, row := range table.Rows[1:] {
		if len(row.Cells) != len(keys) {
			return nil, fmt.Errorf("row has %d cells, want %d", len(row.Cells), len(keys))
		}
		m := make(map[string]string, len(keys))
		for i, key := range keys {
			m[key] = row.Cells[i].Value
		}
		out = append(out, m)
	}
	return out, nil
}

func auditEventsFromTable(table *godog.Table, tenant string) ([]platform.AuditEvent, error) {
	rows, err := credentialTableRows(table)
	if err != nil {
		return nil, err
	}
	out := make([]platform.AuditEvent, 0, len(rows))
	for _, row := range rows {
		at, err := time.Parse(time.RFC3339, row["time"])
		if err != nil {
			return nil, fmt.Errorf("time %q: %w", row["time"], err)
		}
		e := platform.AuditEvent{
			Time:      at,
			Principal: row["principal"],
			Action:    row["action"],
			Resource:  row["resource"],
			Outcome:   row["outcome"],
			RunID:     row["run"],
			Step:      row["step"],
			Reason:    row["reason"],
		}
		if rowTenant, ok := row["tenant"]; ok {
			e.Tenant = core.TenantID(rowTenant)
		} else {
			e.Tenant = core.TenantID(tenant)
		}
		out = append(out, e)
	}
	return out, nil
}

func compareAuditEvents(got, want []platform.AuditEvent) error {
	if len(got) != len(want) {
		return fmt.Errorf("got %d events, want %d: %s", len(got), len(want), formatEvents(got))
	}
	for i := range want {
		g, w := got[i], want[i]
		if !g.Time.Equal(w.Time) || g.Tenant != w.Tenant || g.Principal != w.Principal ||
			g.Action != w.Action || g.Resource != w.Resource || g.Outcome != w.Outcome ||
			g.RunID != w.RunID || g.Step != w.Step || g.Reason != w.Reason {
			return fmt.Errorf("event %d = %s, want %s", i, formatEvent(g), formatEvent(w))
		}
	}
	return nil
}

func formatEvents(events []platform.AuditEvent) string {
	var b bytes.Buffer
	b.WriteString("[")
	for i, e := range events {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(formatEvent(e))
	}
	b.WriteString("]")
	return b.String()
}

func formatEvent(e platform.AuditEvent) string {
	return fmt.Sprintf("{tenant:%s principal:%s action:%s resource:%s run:%s step:%s outcome:%s reason:%s time:%s}",
		e.Tenant, e.Principal, e.Action, e.Resource, e.RunID, e.Step, e.Outcome, e.Reason,
		e.Time.Format(time.RFC3339))
}

func (w *credentialsWorld) auditEvents(tenant string) ([]platform.AuditEvent, error) {
	return w.audits.Read(credentialsCtx(context.Background(), tenant))
}

func credentialSQLCount(w *credentialsWorld, tenant string) (int, error) {
	var n int
	err := w.db.Execer().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM credentials WHERE tenant_id = ?`, tenant).Scan(&n)
	return n, err
}

func registerCredentials(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Credentials; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	sc.Step(`^a credential store over an empty SQL store with master key "([^"]*)"$`, func(ctx context.Context, mk string) error {
		return openCredentialsStore(ctx, mk, false)
	})
	sc.Step(`^a credential store over an empty SQL store and a simulated external store with master key "([^"]*)"$`, func(ctx context.Context, mk string) error {
		return openCredentialsStore(ctx, mk, true)
	})

	sc.Step(`^the value "([^"]*)" is put as "([^"]*)" for tenant "([^"]*)"$`, func(ctx context.Context, value, name, tenant string) error {
		w := credentialsW(ctx)
		w.putErr = w.store.Put(credentialsCtx(ctx, tenant), name, []byte(value))
		w.lastOp = "put"
		setCredentialsError(ctx, w.putErr)
		return nil
	})
	sc.Step(`^the credential "([^"]*)" is deleted for tenant "([^"]*)"$`, func(ctx context.Context, name, tenant string) error {
		w := credentialsW(ctx)
		w.deleteErr = w.store.Delete(credentialsCtx(ctx, tenant), name)
		w.lastOp = "delete"
		setCredentialsError(ctx, w.deleteErr)
		return nil
	})
	sc.Step(`^the credential store puts the value "([^"]*)" under "([^"]*)" without a tenant$`, func(ctx context.Context, value, name string) error {
		w := credentialsW(ctx)
		w.putErr = w.store.Put(context.Background(), name, []byte(value))
		w.lastOp = "put"
		setCredentialsError(ctx, w.putErr)
		return nil
	})
	sc.Step(`^the credential store deletes "([^"]*)" without a tenant$`, func(ctx context.Context, name string) error {
		w := credentialsW(ctx)
		w.deleteErr = w.store.Delete(context.Background(), name)
		w.lastOp = "delete"
		setCredentialsError(ctx, w.deleteErr)
		return nil
	})
	sc.Step(`^the credential store resolves "([^"]*)" without a tenant$`, func(ctx context.Context, ref string) error {
		w := credentialsW(ctx)
		value, err := w.store.Resolve(context.Background(), ref)
		w.lastValue = value
		w.resolveErr = err
		w.lastOp = "resolve"
		setCredentialsError(ctx, err)
		return nil
	})
	sc.Step(`^the credential store lists credentials without a tenant$`, func(ctx context.Context) error {
		w := credentialsW(ctx)
		w.listing, w.listErr = w.store.Names(context.Background())
		w.lastOp = "list"
		setCredentialsError(ctx, w.listErr)
		return nil
	})

	sc.Step(`^the reference "([^"]*)" is resolved for tenant "([^"]*)"$`, func(ctx context.Context, ref, tenant string) error {
		w := credentialsW(ctx)
		value, err := w.store.Resolve(credentialsCtx(ctx, tenant), ref)
		recordResolution(ctx, tenant, value, err)
		return nil
	})
	sc.Step(`^the reference "([^"]*)" is resolved for tenant "([^"]*)" by principal "([^"]*)" in run "([^"]*)" at step "([^"]*)"$`,
		func(ctx context.Context, ref, tenant, principal, run, step string) error {
			w := credentialsW(ctx)
			value, err := w.store.Resolve(credentialsUseCtx(ctx, tenant, principal, run, step), ref)
			recordResolution(ctx, tenant, value, err)
			return nil
		})
	sc.Step(`^principal "([^"]*)" in run "([^"]*)" at step "([^"]*)" resolves "([^"]*)" for tenant "([^"]*)"$`,
		func(ctx context.Context, principal, run, step, ref, tenant string) error {
			value, err := credentialsW(ctx).store.Resolve(
				credentialsUseCtx(ctx, tenant, principal, run, step), ref)
			recordResolution(ctx, tenant, value, err)
			return err
		})
	sc.Step(`^principal "([^"]*)" in run "([^"]*)" at step "([^"]*)" resolves "([^"]*)" for tenant "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, principal, run, step, ref, tenant, want string) error {
			value, err := credentialsW(ctx).store.Resolve(
				credentialsUseCtx(ctx, tenant, principal, run, step), ref)
			recordResolution(ctx, tenant, value, err)
			if err != nil {
				return err
			}
			if string(value) != want {
				return fmt.Errorf("resolved %q, want %q", value, want)
			}
			return nil
		})
	sc.Step(`^a resolution of "([^"]*)" for tenant "([^"]*)" by principal "([^"]*)" in run "([^"]*)" at step "([^"]*)" is waiting for the secrets port$`,
		func(ctx context.Context, ref, tenant, principal, run, step string) error {
			w := credentialsW(ctx)
			gate := &secretsGate{reached: make(chan struct{}), release: make(chan struct{})}
			w.sec.gate = gate
			waiting := &waitingResolution{done: make(chan resolution, 1)}
			w.waiting = waiting
			go func() {
				value, err := w.store.Resolve(credentialsUseCtx(ctx, tenant, principal, run, step), ref)
				waiting.done <- resolution{value: value, err: err}
			}()
			<-gate.reached
			return nil
		})
	sc.Step(`^the waiting resolution is released$`, func(ctx context.Context) error {
		w := credentialsW(ctx)
		if w.waiting == nil {
			return fmt.Errorf("no waiting resolution")
		}
		close(w.sec.gate.release)
		w.waiting.result = <-w.waiting.done
		if w.waiting.result.err != nil {
			return w.waiting.result.err
		}
		return nil
	})
	sc.Step(`^the waiting resolution returns "([^"]*)"$`, func(ctx context.Context, want string) error {
		w := credentialsW(ctx)
		if w.waiting == nil {
			return fmt.Errorf("no waiting resolution")
		}
		if string(w.waiting.result.value) != want {
			return fmt.Errorf("waiting resolution returned %q, want %q", w.waiting.result.value, want)
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" lists its credentials$`, func(ctx context.Context, tenant string) error {
		w := credentialsW(ctx)
		w.listing, w.listErr = w.store.Names(credentialsCtx(ctx, tenant))
		w.lastOp = "list"
		setCredentialsError(ctx, w.listErr)
		return nil
	})
	sc.Step(`^the listing of tenant "([^"]*)" names:$`, func(ctx context.Context, tenant string, table *godog.Table) error {
		rows, err := credentialTableRows(table)
		if err != nil {
			return err
		}
		got := credentialsW(ctx).listing
		if len(got) != len(rows) {
			return fmt.Errorf("listing %v, want %d names", got, len(rows))
		}
		for i, row := range rows {
			if got[i] != row["name"] {
				return fmt.Errorf("listing[%d] = %q, want %q", i, got[i], row["name"])
			}
		}
		return nil
	})
	sc.Step(`^the listing of tenant "([^"]*)" names "([^"]*)"$`, func(ctx context.Context, tenant, name string) error {
		w := credentialsW(ctx)
		names, err := w.store.Names(credentialsCtx(ctx, tenant))
		if err != nil {
			return err
		}
		for _, got := range names {
			if got == name {
				return nil
			}
		}
		return fmt.Errorf("listing %v does not name %q", names, name)
	})
	sc.Step(`^the listing of tenant "([^"]*)" contains no "([^"]*)"$`, func(ctx context.Context, tenant, value string) error {
		for _, name := range credentialsW(ctx).listing {
			if bytes.Contains([]byte(name), []byte(value)) {
				return fmt.Errorf("listing contains %q", value)
			}
		}
		return nil
	})

	sc.Step(`^the resolution returns "([^"]*)"$`, func(ctx context.Context, want string) error {
		w := credentialsW(ctx)
		if w.resolveErr != nil {
			return w.resolveErr
		}
		if string(w.lastValue) != want {
			return fmt.Errorf("resolution returned %q, want %q", w.lastValue, want)
		}
		return nil
	})
	sc.Step(`^the resolution for tenant "([^"]*)" returns "([^"]*)"$`, func(ctx context.Context, tenant, want string) error {
		r, ok := credentialsW(ctx).results[tenant]
		if !ok {
			return fmt.Errorf("no resolution for tenant %q", tenant)
		}
		if r.err != nil {
			return r.err
		}
		if string(r.value) != want {
			return fmt.Errorf("resolution for %q returned %q, want %q", tenant, r.value, want)
		}
		return nil
	})
	sc.Step(`^the deletion fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return credentialsCode(credentialsW(ctx).deleteErr, code)
	})
	sc.Step(`^the listing fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return credentialsCode(credentialsW(ctx).listErr, code)
	})

	sc.Step(`^the stored credential of tenant "([^"]*)" for "([^"]*)" contains no plaintext of "([^"]*)"$`,
		func(ctx context.Context, tenant, name, value string) error {
			rec, err := credentials.NewSQLBackend(credentialsW(ctx).db).Get(credentialsCtx(ctx, tenant), name)
			if err != nil {
				return err
			}
			if bytes.Contains(rec.Ciphertext, []byte(value)) {
				return fmt.Errorf("stored ciphertext contains plaintext")
			}
			return nil
		})
	sc.Step(`^the stored ciphertext of tenant "([^"]*)" for "([^"]*)" decrypts through the secrets port to "([^"]*)"$`,
		func(ctx context.Context, tenant, name, want string) error {
			w := credentialsW(ctx)
			rec, err := credentials.NewSQLBackend(w.db).Get(credentialsCtx(ctx, tenant), name)
			if err != nil {
				return err
			}
			value, err := w.sec.Decrypt(credentialsCtx(ctx, tenant), rec.Ciphertext)
			if err != nil {
				return err
			}
			if string(value) != want {
				return fmt.Errorf("decrypted %q, want %q", value, want)
			}
			return nil
		})
	sc.Step(`^the stored ciphertext of tenant "([^"]*)" for "([^"]*)" is altered$`, func(ctx context.Context, tenant, name string) error {
		w := credentialsW(ctx)
		rec, err := credentials.NewSQLBackend(w.db).Get(credentialsCtx(ctx, tenant), name)
		if err != nil {
			return err
		}
		ct := append([]byte(nil), rec.Ciphertext...)
		if len(ct) > 0 {
			ct[0] ^= 1
		}
		_, err = w.db.Execer().Exec(ctx,
			`UPDATE credentials SET ciphertext = ? WHERE tenant_id = ? AND name = ?`,
			ct, tenant, name)
		return err
	})
	sc.Step(`^the credential store holds exactly (\d+) stored credentials? for tenant "([^"]*)"$`,
		func(ctx context.Context, n int, tenant string) error {
			got, err := credentialSQLCount(credentialsW(ctx), tenant)
			if err != nil {
				return err
			}
			if got != n {
				return fmt.Errorf("tenant %q holds %d credentials, want %d", tenant, got, n)
			}
			return nil
		})
	sc.Step(`^the credential store reopens on the same SQL store and master key "([^"]*)"$`, reopenCredentialsStore)
	sc.Step(`^no credential is stored for any tenant$`, func(ctx context.Context) error {
		var n int
		if err := credentialsW(ctx).db.Execer().QueryRow(context.Background(),
			`SELECT COUNT(*) FROM credentials`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("credentials table holds %d rows, want 0", n)
		}
		return nil
	})

	sc.Step(`^the simulated external store holds exactly (\d+) credentials? for tenant "([^"]*)"$`,
		func(ctx context.Context, n int, tenant string) error {
			got := credentialsW(ctx).ext.count(tenant)
			if got != n {
				return fmt.Errorf("external store holds %d for %q, want %d", got, tenant, n)
			}
			return nil
		})
	sc.Step(`^the simulated external store holds no plaintext of "([^"]*)"$`, func(ctx context.Context, value string) error {
		if credentialsW(ctx).ext.containsPlaintext(value) {
			return fmt.Errorf("external store holds plaintext %q", value)
		}
		return nil
	})
	sc.Step(`^the stored ciphertext in the simulated external store decrypts through the secrets port to "([^"]*)"$`,
		func(ctx context.Context, want string) error {
			ct, ok := credentialsW(ctx).ext.ciphertext("acme", "vendor/anthropic")
			if !ok {
				return fmt.Errorf("external store has no credential")
			}
			value, err := credentialsW(ctx).sec.Decrypt(context.Background(), ct)
			if err != nil {
				return err
			}
			if string(value) != want {
				return fmt.Errorf("decrypted %q, want %q", value, want)
			}
			return nil
		})
	sc.Step(`^the SQL store holds no credential for tenant "([^"]*)"$`, func(ctx context.Context, tenant string) error {
		got, err := credentialSQLCount(credentialsW(ctx), tenant)
		if err != nil {
			return err
		}
		if got != 0 {
			return fmt.Errorf("SQL store holds %d credentials for %q", got, tenant)
		}
		return nil
	})

	sc.Step(`^the audit port received (\d+) events$`, func(ctx context.Context, n int) error {
		if got := credentialsW(ctx).audit.count(); got != n {
			return fmt.Errorf("audit port received %d events, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the audit store rejects every record$`, func(ctx context.Context) error {
		w := credentialsW(ctx)
		w.audit.mu.Lock()
		w.audit.reject = true
		w.audit.mu.Unlock()
		return nil
	})
	sc.Step(`^the audit store reopens on the same SQL store$`, func(ctx context.Context) error {
		w := credentialsW(ctx)
		w.audits = credentials.NewSQLAudit(w.db)
		return nil
	})
	sc.Step(`^tenant "([^"]*)" reads its audit events$`, func(ctx context.Context, tenant string) error {
		w := credentialsW(ctx)
		_, w.auditErr = w.audits.Read(credentialsCtx(ctx, tenant))
		setCredentialsError(ctx, w.auditErr)
		return w.auditErr
	})
	sc.Step(`^the audit log of tenant "([^"]*)" is exactly:$`, func(ctx context.Context, tenant string, table *godog.Table) error {
		want, err := auditEventsFromTable(table, tenant)
		if err != nil {
			return err
		}
		got, err := credentialsW(ctx).auditEvents(tenant)
		if err != nil {
			return err
		}
		return compareAuditEvents(got, want)
	})
	sc.Step(`^the audit log of tenant "([^"]*)" is empty$`, func(ctx context.Context, tenant string) error {
		got, err := credentialsW(ctx).auditEvents(tenant)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			return fmt.Errorf("audit log holds %s", formatEvents(got))
		}
		return nil
	})
	sc.Step(`^the audit store of tenant "([^"]*)" holds exactly (\d+) events?:$`, func(ctx context.Context, tenant string, n int, table *godog.Table) error {
		want, err := auditEventsFromTable(table, tenant)
		if err != nil {
			return err
		}
		got, err := credentialsW(ctx).auditEvents(tenant)
		if err != nil {
			return err
		}
		if len(want) != n {
			return fmt.Errorf("table names %d events, step says %d", len(want), n)
		}
		return compareAuditEvents(got, want)
	})
	sc.Step(`^the audit events read for tenant "([^"]*)" are exactly:$`, func(ctx context.Context, tenant string, table *godog.Table) error {
		want, err := auditEventsFromTable(table, tenant)
		if err != nil {
			return err
		}
		got, err := credentialsW(ctx).auditEvents(tenant)
		if err != nil {
			return err
		}
		return compareAuditEvents(got, want)
	})
	sc.Step(`^the audit events read for tenant "([^"]*)" are empty$`, func(ctx context.Context, tenant string) error {
		got, err := credentialsW(ctx).auditEvents(tenant)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			return fmt.Errorf("audit read returned %s", formatEvents(got))
		}
		return nil
	})
	sc.Step(`^the audit table holds (\d+) deployment-level event for action "([^"]*)" and resource "([^"]*)"$`,
		func(ctx context.Context, n int, action, resource string) error {
			var got int
			err := credentialsW(ctx).db.Execer().QueryRow(ctx,
				`SELECT COUNT(*) FROM audit_events WHERE tenant_id = '' AND action = ? AND resource = ?`,
				action, resource).Scan(&got)
			if err != nil {
				return err
			}
			if got != n {
				return fmt.Errorf("audit table holds %d deployment-level events, want %d", got, n)
			}
			return nil
		})
	sc.Step(`^audit events are read from the audit store without a tenant$`, func(ctx context.Context) error {
		w := credentialsW(ctx)
		_, w.auditErr = w.audits.Read(context.Background())
		setCredentialsError(ctx, w.auditErr)
		return nil
	})
	sc.Step(`^the audit store call fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return credentialsCode(credentialsW(ctx).auditErr, code)
	})
	sc.Step(`^the audit store records a "([^"]*)" event with tenant "([^"]*)" and without a tenant in context$`,
		func(ctx context.Context, action, tenant string) error {
			w := credentialsW(ctx)
			w.auditErr = w.audits.Record(context.Background(), platform.AuditEvent{
				Time: world(ctx).Core.clock.Now(), Tenant: core.TenantID(tenant),
				Action: action, Resource: "cred://vendor/anthropic", Outcome: "allowed",
			})
			setCredentialsError(ctx, w.auditErr)
			return nil
		})
	sc.Step(`^the audit store records a "([^"]*)" event with tenant "([^"]*)" under context tenant "([^"]*)"$`,
		func(ctx context.Context, action, claimed, tenant string) error {
			w := credentialsW(ctx)
			w.auditErr = w.audits.Record(credentialsCtx(ctx, tenant), platform.AuditEvent{
				Time: world(ctx).Core.clock.Now(), Tenant: core.TenantID(claimed),
				Action: action, Resource: "cred://vendor/anthropic", Outcome: "allowed",
			})
			setCredentialsError(ctx, w.auditErr)
			return nil
		})
	sc.Step(`^the audit store records a "([^"]*)" event for pool "([^"]*)" with an empty tenant and without a tenant in context$`,
		func(ctx context.Context, action, pool string) error {
			w := credentialsW(ctx)
			w.auditErr = w.audits.Record(context.Background(), platform.AuditEvent{
				Time: world(ctx).Core.clock.Now(), Action: action, Resource: pool, Outcome: "failed",
			})
			setCredentialsError(ctx, w.auditErr)
			return nil
		})

	sc.Step(`^neither the audit log of tenant "([^"]*)" nor the captured log contains "([^"]*)"$`,
		func(ctx context.Context, tenant, value string) error {
			w := credentialsW(ctx)
			events, err := w.auditEvents(tenant)
			if err != nil {
				return err
			}
			if bytes.Contains([]byte(formatEvents(events)), []byte(value)) {
				return fmt.Errorf("audit log contains %q", value)
			}
			if bytes.Contains(secretsW(ctx).logBuf.Bytes(), []byte(value)) {
				return fmt.Errorf("log contains %q", value)
			}
			return nil
		})
	sc.Step(`^neither the error nor the audit log of tenant "([^"]*)" nor the captured log contains "([^"]*)"$`,
		func(ctx context.Context, tenant, value string) error {
			w := credentialsW(ctx)
			if w.lastErr != nil && bytes.Contains([]byte(w.lastErr.Error()), []byte(value)) {
				return fmt.Errorf("error contains %q", value)
			}
			events, err := w.auditEvents(tenant)
			if err != nil {
				return err
			}
			if bytes.Contains([]byte(formatEvents(events)), []byte(value)) {
				return fmt.Errorf("audit log contains %q", value)
			}
			if bytes.Contains(secretsW(ctx).logBuf.Bytes(), []byte(value)) {
				return fmt.Errorf("log contains %q", value)
			}
			return nil
		})
}

func credentialsCode(err error, code string) error {
	if err == nil {
		return fmt.Errorf("succeeded, want code %s", code)
	}
	if got := core.CodeOf(err); string(got) != code {
		return fmt.Errorf("got code %s, want %s: %w", got, code, err)
	}
	return nil
}
