package steps

import (
	"context"
	"testing"

	"github.com/epzero/basamento/platform/blob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime/temporal"
)

func TestRecordingConverterUsesContextTenantAndRejectsWrongTenant(t *testing.T) {
	reg := newSimRegistry()
	reg.hold("mk1")
	enc, err := secrets.NewCodec(newBDDKeysetStore(), []secrets.KeyService{&simKeyService{reg: reg, name: "mk1"}})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := blob.Open(context.Background(), "file://"+t.TempDir(), blob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := temporal.New(temporal.Options{Payloads: enc, Blobs: st, Outbox: newMemOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	c := &recordingConverter{inner: rt.DataConverter(), enc: enc, tenant: "fallback"}
	acme := core.WithTenant(context.Background(), "acme")
	p, err := c.WithContext(acme).ToPayload("secret")
	if err != nil {
		t.Fatal(err)
	}
	var out string
	if err := c.WithContext(acme).FromPayload(p, &out); err != nil {
		t.Fatalf("context tenant not used: %v", err)
	}
	if out != "secret" {
		t.Fatalf("decoded %q want secret", out)
	}
	beta := core.WithTenant(context.Background(), "beta")
	if err := c.WithContext(beta).FromPayload(p, &out); err == nil {
		t.Fatal("payload decoded for the wrong tenant")
	}
}
