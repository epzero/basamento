package secrets_test

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

type spyKeysetStore struct {
	get, create, cas, countCalls, tenantsCalls int
}

func (s *spyKeysetStore) Get(string) (secrets.StoredKeyset, bool, error) {
	s.get++
	return secrets.StoredKeyset{}, false, nil
}

func (s *spyKeysetStore) CreateIfAbsent(string, secrets.StoredKeyset) (bool, error) {
	s.create++
	return false, nil
}

func (s *spyKeysetStore) CompareAndSwap(string, uint64, secrets.StoredKeyset) error {
	s.cas++
	return nil
}

func (s *spyKeysetStore) Count() (int, error) {
	s.countCalls++
	return 0, nil
}

func (s *spyKeysetStore) Tenants() ([]string, error) {
	s.tenantsCalls++
	return nil, nil
}

type stubKeyService struct{}

func (stubKeyService) MasterKeyID() string { return "stub" }

func (stubKeyService) Wrap(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func (stubKeyService) Unwrap(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func TestCodecRejectsMalformedTenantBeforeStore(t *testing.T) {
	t.Parallel()
	malformed := []string{"", "acme\x00evil", strings.Repeat("x", 129), "../acme", "a/b"}
	ctx := context.Background()
	for _, tenant := range malformed {
		store := &spyKeysetStore{}
		c, err := secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.RotateDataKey(ctx, tenant); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("RotateDataKey(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("RotateDataKey(%q): store Get %d times", tenant, store.get)
		}
		store = &spyKeysetStore{}
		c, _ = secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if _, err := c.PrimaryDataKeyID(ctx, tenant); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("PrimaryDataKeyID(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("PrimaryDataKeyID(%q): store Get %d times", tenant, store.get)
		}
		store = &spyKeysetStore{}
		c, _ = secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if _, err := c.DataKeyCount(ctx, tenant); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("DataKeyCount(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("DataKeyCount(%q): store Get %d times", tenant, store.get)
		}
	}
}
