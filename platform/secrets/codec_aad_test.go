package secrets_test

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

type aadStore struct {
	data map[string]secrets.StoredKeyset
}

func (a *aadStore) Get(context.Context, string) (secrets.StoredKeyset, bool, error) {
	return secrets.StoredKeyset{}, false, nil
}
func (a *aadStore) CreateIfAbsent(_ context.Context, tenant string, r secrets.StoredKeyset) (bool, error) {
	a.data[tenant] = r
	return true, nil
}
func (a *aadStore) CompareAndSwap(context.Context, string, uint64, secrets.StoredKeyset) error {
	return nil
}
func (a *aadStore) Count(context.Context) (int, error)        { return len(a.data), nil }
func (a *aadStore) Tenants(context.Context) ([]string, error) { return nil, nil }

type aadKey struct{ id string }

func (k aadKey) MasterKeyID() string { return k.id }
func (k aadKey) Close() error        { return nil }
func (k aadKey) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	return secrets.FrameBoundPlaintext(tenant, plain)
}
func (k aadKey) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	return secrets.ParseBoundPlaintext(tenant, ct)
}

func TestPayloadTenantAADEnforced(t *testing.T) {
	t.Parallel()
	store := &aadStore{data: map[string]secrets.StoredKeyset{}}
	ks := aadKey{id: "mk"}
	c, err := secrets.NewCodec(store, []secrets.KeyService{ks})
	if err != nil {
		t.Fatal(err)
	}
	ctxAcme := core.WithTenant(context.Background(), "acme")
	ctxGlobex := core.WithTenant(context.Background(), "globex")
	p := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("secret")}
	enc, err := c.Encode(ctxAcme, p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(ctxGlobex, enc)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("cross-tenant decode: %v", err)
	}
}
