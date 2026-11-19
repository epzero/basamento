package secrets

import (
	"context"
	"testing"
)

type closeCountKeyService struct {
	id     string
	closed int
}

func (k *closeCountKeyService) MasterKeyID() string { return k.id }

func (k *closeCountKeyService) Wrap(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}

func (k *closeCountKeyService) Unwrap(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}

func (k *closeCountKeyService) Close() error {
	k.closed++
	return nil
}

func TestPrependMasterKeyClosesDroppedKeyService(t *testing.T) {
	t.Parallel()
	old := &closeCountKeyService{id: "master-a"}
	nu := &closeCountKeyService{id: "master-a"}
	c := &Codec{keys: []KeyService{old}}
	c.prependMasterKeyLocked(nu)
	if old.closed != 1 {
		t.Fatalf("dropped key Close calls=%d want 1", old.closed)
	}
	if nu.closed != 0 {
		t.Fatalf("new key Close calls=%d want 0", nu.closed)
	}
	if len(c.keys) != 1 || c.keys[0] != nu {
		t.Fatalf("keys=%v", c.keys)
	}
}
