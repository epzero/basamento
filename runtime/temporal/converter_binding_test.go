package temporal

import (
	"context"
	"testing"

	"go.temporal.io/sdk/converter"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
)

func TestDataConverterWithWorkflowContextClearsStaleGoContext(t *testing.T) {
	codec, err := newPayloadCodec(testSecretsCodec(t), testBlobStore(t), DefaultClaimCheckBytes)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := newDataConverter(codec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctxA := core.WithTenant(context.Background(), "tenant-a")
	aware := dc.(interface {
		WithContext(context.Context) converter.DataConverter
		WithWorkflowContext(sdkworkflow.Context) converter.DataConverter
	})
	wfCtx := sdkworkflow.WithValue(nil, tenantWorkflowKey{}, core.TenantID("tenant-b"))
	afterCtx := aware.WithContext(ctxA).(interface {
		WithWorkflowContext(sdkworkflow.Context) converter.DataConverter
	})
	chained := afterCtx.WithWorkflowContext(wfCtx).(*dataConverter)
	if chained.ctx != nil {
		t.Fatal("stale Go context not cleared")
	}
	tid, _, err := chained.tenantBase()
	if err != nil {
		t.Fatal(err)
	}
	if tid != "tenant-b" {
		t.Fatalf("tenant %q want tenant-b", tid)
	}
}
