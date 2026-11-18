package secrets_test

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

type spyKeysetStore struct {
	get, create, cas, countCalls, tenantsCalls int
}

func (s *spyKeysetStore) Get(context.Context, string) (secrets.StoredKeyset, bool, error) {
	s.get++
	return secrets.StoredKeyset{}, false, nil
}

func (s *spyKeysetStore) CreateIfAbsent(context.Context, string, secrets.StoredKeyset) (bool, error) {
	s.create++
	return false, nil
}

func (s *spyKeysetStore) CompareAndSwap(context.Context, string, uint64, secrets.StoredKeyset) error {
	s.cas++
	return nil
}

func (s *spyKeysetStore) Count(context.Context) (int, error) {
	s.countCalls++
	return 0, nil
}

func (s *spyKeysetStore) Tenants(context.Context) ([]string, error) {
	s.tenantsCalls++
	return nil, nil
}

type stubKeyService struct{}

func (stubKeyService) MasterKeyID() string { return "stub" }

func (stubKeyService) Wrap(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func (stubKeyService) Unwrap(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func (stubKeyService) Close() error { return nil }

func TestCodecRejectsMalformedTenantBeforeStore(t *testing.T) {
	t.Parallel()
	malformed := []string{"", "acme\x00evil", stringsRepeat('x', 129), "../acme", "a/b"}
	for _, tenant := range malformed {
		store := &spyKeysetStore{}
		c, err := secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
		if err := c.RotateDataKey(ctx); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("RotateDataKey(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("RotateDataKey(%q): store Get %d times", tenant, store.get)
		}
		store = &spyKeysetStore{}
		c, _ = secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if _, err := c.PrimaryDataKeyID(ctx); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("PrimaryDataKeyID(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("PrimaryDataKeyID(%q): store Get %d times", tenant, store.get)
		}
		store = &spyKeysetStore{}
		c, _ = secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if _, err := c.DataKeyCount(ctx); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("DataKeyCount(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("DataKeyCount(%q): store Get %d times", tenant, store.get)
		}
	}
}

func TestCodecTenantIsolationNoCrossTenantMutate(t *testing.T) {
	t.Parallel()
	store := &spyKeysetStore{}
	c, err := secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
	if err != nil {
		t.Fatal(err)
	}
	ctxB := core.WithTenant(context.Background(), "globex")
	_ = c.RotateDataKey(ctxB)
	_, _ = c.PrimaryDataKeyID(ctxB)
	_, _ = c.DataKeyCount(ctxB)
	if store.create != 0 || store.cas != 0 {
		t.Fatalf("store touched: create=%d cas=%d", store.create, store.cas)
	}
}

func stringsRepeat(s byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = s
	}
	return string(b)
}
