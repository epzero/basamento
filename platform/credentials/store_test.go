package credentials

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

var testTime = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func openTestDB(t *testing.T) (*psql.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.db")
	s, err := psql.Open(t.Context(), "sqlite:///"+path, Migrations())
	if err != nil {
		t.Fatalf("open sql store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// xorSecrets is a deterministic platform.Secrets: the ciphertext never
// contains its plaintext and carries no key state.
type xorSecrets struct {
	mu         sync.Mutex
	encryptErr error
	decryptErr error
	decrypts   int

	reached chan struct{}
	release chan struct{}
	abort   chan struct{}
	once    sync.Once
}

func newXORSecrets() *xorSecrets { return &xorSecrets{} }

func (s *xorSecrets) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	s.mu.Lock()
	err := s.encryptErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(plaintext)+1)
	out[0] = 0xA5
	for i, b := range plaintext {
		out[i+1] = b ^ 0x5A
	}
	return out, nil
}

func (s *xorSecrets) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	s.decrypts++
	s.mu.Unlock()
	if s.reached != nil {
		s.once.Do(func() { close(s.reached) })
		select {
		case <-s.release:
		case <-s.abort:
			return nil, core.NewError(core.CodeUnavailable, "resolution aborted", nil)
		}
	}
	s.mu.Lock()
	err := s.decryptErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || ciphertext[0] != 0xA5 {
		return nil, core.NewError(core.CodeInvalidArgument, "cannot decrypt", nil)
	}
	out := make([]byte, len(ciphertext)-1)
	for i, b := range ciphertext[1:] {
		out[i] = b ^ 0x5A
	}
	return out, nil
}

type fakeAudit struct {
	mu     sync.Mutex
	events []platform.AuditEvent
	err    error
	calls  int
}

func (a *fakeAudit) Record(_ context.Context, e platform.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, e)
	return nil
}

func (a *fakeAudit) snapshot() ([]platform.AuditEvent, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]platform.AuditEvent(nil), a.events...), a.calls
}

type testStore struct {
	store   *Store
	sql     *psql.Store
	secrets *xorSecrets
	audit   *fakeAudit
	clock   *coretest.FakeClock
}

func newTestStore(t *testing.T) *testStore {
	t.Helper()
	db, _ := openTestDB(t)
	return newTestStoreOver(t, db)
}

func newTestStoreOver(t *testing.T, db *psql.Store) *testStore {
	t.Helper()
	sec := newXORSecrets()
	audit := &fakeAudit{}
	clock := coretest.NewFakeClock(testTime)
	return &testStore{
		store:   New(NewSQLBackend(db), audit, sec, clock),
		sql:     db,
		secrets: sec,
		audit:   audit,
		clock:   clock,
	}
}

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func mustPut(t *testing.T, s *Store, tenant, name, value string) {
	t.Helper()
	if err := s.Put(tenantCtx(tenant), name, []byte(value)); err != nil {
		t.Fatalf("Put(%s, %s): %v", tenant, name, err)
	}
}

func mustResolve(t *testing.T, s *Store, tenant, ref string) string {
	t.Helper()
	got, err := s.Resolve(tenantCtx(tenant), ref)
	if err != nil {
		t.Fatalf("Resolve(%s, %s): %v", tenant, ref, err)
	}
	return string(got)
}

func wantCode(t *testing.T, err error, code core.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("succeeded, want code %s", code)
	}
	if got := core.CodeOf(err); got != code {
		t.Fatalf("code %s, want %s: %v", got, code, err)
	}
}

func TestRoundTripAndReplace(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-old")
	if got := mustResolve(t, ts.store, "acme", "cred://vendor/anthropic"); got != "sk-live-old" {
		t.Fatalf("got %q", got)
	}
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-new")
	if got := mustResolve(t, ts.store, "acme", "cred://vendor/anthropic"); got != "sk-live-new" {
		t.Fatalf("replace: got %q", got)
	}
}

func TestDelete(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "v")
	if err := ts.store.Delete(tenantCtx("acme"), "vendor/anthropic"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := ts.store.Resolve(tenantCtx("acme"), "cred://vendor/anthropic")
	wantCode(t, err, core.CodeNotFound)
	wantCode(t, ts.store.Delete(tenantCtx("acme"), "vendor/absent"), core.CodeNotFound)
}

func TestTenantRequired(t *testing.T) {
	ts := newTestStore(t)
	wantCode(t, ts.store.Put(context.Background(), "a", []byte("v")), core.CodeInvalidArgument)
	_, err := ts.store.Resolve(context.Background(), "cred://a")
	wantCode(t, err, core.CodeInvalidArgument)
	wantCode(t, ts.store.Delete(context.Background(), "a"), core.CodeInvalidArgument)
	_, err = ts.store.Names(context.Background())
	wantCode(t, err, core.CodeInvalidArgument)
}

func TestTenantIsolation(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/x", "sk-acme")
	mustPut(t, ts.store, "globex", "vendor/x", "sk-globex")
	if got := mustResolve(t, ts.store, "acme", "cred://vendor/x"); got != "sk-acme" {
		t.Fatalf("acme got %q", got)
	}
	if got := mustResolve(t, ts.store, "globex", "cred://vendor/x"); got != "sk-globex" {
		t.Fatalf("globex got %q", got)
	}
	_, err := ts.store.Resolve(tenantCtx("other"), "cred://vendor/x")
	wantCode(t, err, core.CodeNotFound)
	if err := ts.store.Delete(tenantCtx("globex"), "vendor/x"); err != nil {
		t.Fatalf("globex delete: %v", err)
	}
	if got := mustResolve(t, ts.store, "acme", "cred://vendor/x"); got != "sk-acme" {
		t.Fatalf("acme after globex delete: %q", got)
	}
}

func TestNamesScopedAndOrdered(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/b", "x")
	mustPut(t, ts.store, "acme", "vendor/a", "y")
	mustPut(t, ts.store, "acme", "Vendor/c", "z")
	mustPut(t, ts.store, "globex", "vendor/g", "g")
	names, err := ts.store.Names(tenantCtx("acme"))
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	want := []string{"Vendor/c", "vendor/a", "vendor/b"}
	if len(names) != len(want) {
		t.Fatalf("names %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names %v want %v", names, want)
		}
	}
}

func (s *xorSecrets) decryptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decrypts
}

func TestWriteOnlyOperationsDoNotDecrypt(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/a", "v")
	if _, err := ts.store.Names(tenantCtx("acme")); err != nil {
		t.Fatal(err)
	}
	if err := ts.store.Delete(tenantCtx("acme"), "vendor/a"); err != nil {
		t.Fatal(err)
	}
	if got := ts.secrets.decryptCount(); got != 0 {
		t.Fatalf("Put, Names and Delete called Decrypt %d times", got)
	}
}

func TestNamesNeverReturnValues(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/a", "sk-secret")
	names, err := ts.store.Names(tenantCtx("acme"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if bytes.Contains([]byte(name), []byte("sk-secret")) {
			t.Fatalf("name contains value: %q", name)
		}
	}
}

func TestEncryptedAtRest(t *testing.T) {
	ts := newTestStore(t)
	const value = "sk-live-4f9a2c"
	mustPut(t, ts.store, "acme", "vendor/anthropic", value)
	var ct []byte
	err := ts.sql.Execer().QueryRow(context.Background(),
		`SELECT ciphertext FROM credentials WHERE tenant_id = ? AND name = ?`,
		"acme", "vendor/anthropic").Scan(&ct)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if bytes.Contains(ct, []byte(value)) {
		t.Fatal("stored ciphertext contains plaintext")
	}
	pt, err := ts.secrets.Decrypt(context.Background(), ct)
	if err != nil || string(pt) != value {
		t.Fatalf("decrypt row: %q, %v", pt, err)
	}
}

func TestRestartSurvives(t *testing.T) {
	db, path := openTestDB(t)
	ts := newTestStoreOver(t, db)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-4f9a2c")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := psql.Open(context.Background(), "sqlite:///"+path, Migrations())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	ts2 := newTestStoreOver(t, reopened)
	if got := mustResolve(t, ts2.store, "acme", "cred://vendor/anthropic"); got != "sk-live-4f9a2c" {
		t.Fatalf("after restart got %q", got)
	}
}

func TestUndecryptableStoredCredential(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-4f9a2c")
	_, err := ts.sql.Execer().Exec(context.Background(),
		`UPDATE credentials SET ciphertext = ? WHERE tenant_id = ? AND name = ?`,
		[]byte{0x00}, "acme", "vendor/anthropic")
	if err != nil {
		t.Fatalf("alter: %v", err)
	}
	ctx := WithUse(tenantCtx("acme"), Use{Principal: "human:ana", RunID: "r1", StepID: "s2"})
	value, err := ts.store.Resolve(ctx, "cred://vendor/anthropic")
	wantCode(t, err, core.CodeInternal)
	if value != nil {
		t.Fatalf("value returned: %q", value)
	}
	events, _ := ts.audit.snapshot()
	if len(events) != 1 {
		t.Fatalf("events %+v", events)
	}
	e := events[0]
	if e.Outcome != "failed" || e.Reason != "internal" || e.Resource != "cred://vendor/anthropic" {
		t.Fatalf("event %+v", e)
	}
}

func TestSecretsUnavailable(t *testing.T) {
	ts := newTestStore(t)
	ts.secrets.encryptErr = &core.Error{Code: core.CodeUnavailable, Message: "localsecrets: unavailable"}
	wantCode(t, ts.store.Put(tenantCtx("acme"), "a", []byte("v")), core.CodeUnavailable)

	ts.secrets.encryptErr = nil
	mustPut(t, ts.store, "acme", "a", "v")
	ts.secrets.decryptErr = &core.Error{Code: core.CodeUnavailable, Message: "localsecrets: unavailable"}
	_, err := ts.store.Resolve(tenantCtx("acme"), "cred://a")
	wantCode(t, err, core.CodeUnavailable)
	events, _ := ts.audit.snapshot()
	if len(events) != 1 || events[0].Reason != "unavailable" {
		t.Fatalf("events %+v", events)
	}
}

func TestAuditFields(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "v")
	ctx := WithUse(tenantCtx("acme"), Use{Principal: "human:ana", RunID: "r1", StepID: "s2"})
	if _, err := ts.store.Resolve(ctx, "cred://vendor/anthropic"); err != nil {
		t.Fatal(err)
	}
	events, calls := ts.audit.snapshot()
	if calls != 1 || len(events) != 1 {
		t.Fatalf("calls %d events %+v", calls, events)
	}
	e := events[0]
	want := platform.AuditEvent{
		Time:      testTime,
		Tenant:    "acme",
		Principal: "human:ana",
		Action:    "credential.resolve",
		Resource:  "cred://vendor/anthropic",
		Outcome:   "allowed",
		RunID:     "r1",
		Step:      "s2",
	}
	if e != want {
		t.Fatalf("event %+v want %+v", e, want)
	}
}

func TestAuditEmptyUse(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "a", "v")
	if got := mustResolve(t, ts.store, "acme", "cred://a"); got != "v" {
		t.Fatal(got)
	}
	events, _ := ts.audit.snapshot()
	if len(events) != 1 || events[0].Principal != "" || events[0].RunID != "" || events[0].Step != "" {
		t.Fatalf("events %+v", events)
	}
}

func TestAuditFailedLookup(t *testing.T) {
	ts := newTestStore(t)
	ctx := WithUse(tenantCtx("acme"), Use{Principal: "human:ana", RunID: "r1", StepID: "s2"})
	_, err := ts.store.Resolve(ctx, "cred://vendor/absent")
	wantCode(t, err, core.CodeNotFound)
	events, _ := ts.audit.snapshot()
	if len(events) != 1 || events[0].Outcome != "failed" || events[0].Reason != "not_found" {
		t.Fatalf("events %+v", events)
	}
}

func TestMalformedReferenceIsNotAudited(t *testing.T) {
	ts := newTestStore(t)
	ctx := WithUse(tenantCtx("acme"), Use{Principal: "human:ana"})
	_, err := ts.store.Resolve(ctx, "vault://vendor/anthropic")
	wantCode(t, err, core.CodeInvalidArgument)
	if bytes.Contains([]byte(err.Error()), []byte("vault://")) {
		t.Fatalf("error echoes reference: %v", err)
	}
	if _, calls := ts.audit.snapshot(); calls != 0 {
		t.Fatalf("audit calls %d", calls)
	}
}

func TestNoTenantIsNotAudited(t *testing.T) {
	ts := newTestStore(t)
	_, err := ts.store.Resolve(context.Background(), "cred://a")
	wantCode(t, err, core.CodeInvalidArgument)
	if _, calls := ts.audit.snapshot(); calls != 0 {
		t.Fatalf("audit calls %d", calls)
	}
}

func TestAuditFailureFailsClosed(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-4f9a2c")
	ts.audit.err = errors.New("audit down")
	value, err := ts.store.Resolve(tenantCtx("acme"), "cred://vendor/anthropic")
	wantCode(t, err, core.CodeUnavailable)
	if value != nil {
		t.Fatalf("value returned: %q", value)
	}
	if bytes.Contains([]byte(err.Error()), []byte("sk-live")) {
		t.Fatalf("error leaks value: %v", err)
	}
}

func TestRotationAtNextUse(t *testing.T) {
	ts := newTestStore(t)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-old")
	_ = mustResolve(t, ts.store, "acme", "cred://vendor/anthropic")
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-new")
	if got := mustResolve(t, ts.store, "acme", "cred://vendor/anthropic"); got != "sk-live-new" {
		t.Fatalf("got %q", got)
	}
}

func TestRotationDuringResolution(t *testing.T) {
	ts := newTestStore(t)
	ts.secrets.reached = make(chan struct{})
	ts.secrets.release = make(chan struct{})
	ts.secrets.abort = make(chan struct{})
	// A resolution that never reaches the secrets port must not hang the test.
	defer close(ts.secrets.abort)
	mustPut(t, ts.store, "acme", "vendor/anthropic", "sk-live-old")

	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := ts.store.Resolve(
			WithUse(tenantCtx("acme"), Use{Principal: "human:ana", RunID: "r1", StepID: "s1"}),
			"cred://vendor/anthropic")
		done <- result{string(value), err}
	}()
	select {
	case <-ts.secrets.reached:
	case got := <-done:
		t.Fatalf("resolution finished before it reached the secrets port: (%q, %v)", got.value, got.err)
	}

	if err := ts.store.Put(tenantCtx("acme"), "vendor/anthropic", []byte("sk-live-new")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	close(ts.secrets.release)

	got := <-done
	if got.err != nil || got.value != "sk-live-old" {
		t.Fatalf("in-flight resolution = (%q, %v), want old", got.value, got.err)
	}
	if next := mustResolve(t, ts.store, "acme", "cred://vendor/anthropic"); next != "sk-live-new" {
		t.Fatalf("later resolution = %q", next)
	}
}

func TestConcurrentUse(t *testing.T) {
	ts := newTestStore(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n*2)
	for i := 0; i < n; i++ {
		name := "vendor/" + string(rune('a'+i))
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := ts.store.Put(tenantCtx("acme"), name, []byte(name)); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			value, err := ts.store.Resolve(tenantCtx("acme"), "cred://"+name)
			if err == nil && string(value) != name {
				errs <- errors.New("wrong value for " + name)
			}
			if err != nil && core.CodeOf(err) != core.CodeNotFound {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	names, err := ts.store.Names(tenantCtx("acme"))
	if err != nil || len(names) != n {
		t.Fatalf("names %v, %v", names, err)
	}
}

func TestUseAccessors(t *testing.T) {
	ctx := context.Background()
	if got := UseFrom(ctx); got != (Use{}) {
		t.Fatalf("empty = %+v", got)
	}
	u := Use{Principal: "human:ana", RunID: "r1", StepID: "s1"}
	ctx = WithUse(ctx, u)
	if got := UseFrom(ctx); got != u {
		t.Fatalf("got %+v", got)
	}
}
