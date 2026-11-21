package temporal

import (
	"context"
	"testing"

	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
)

// tenantBase must reject malformed ids from either source before the codec
// sees them. A nil codec makes a codec call panic, so a returned error proves
// the converter stopped at the tenant check.
func TestDataConverterRejectsMalformedTenantBeforeCodec(t *testing.T) {
	const bad = core.TenantID("acme/eu")
	cases := []struct {
		name  string
		goCtx bool
		wfCtx bool
	}{
		{"go_context", true, false},
		{"workflow_context", false, true},
		{"both", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &dataConverter{}
			if tc.goCtx {
				d.ctx = core.WithTenant(context.Background(), bad)
			}
			if tc.wfCtx {
				d.wfCtx = sdkworkflow.WithValue(nil, tenantWorkflowKey{}, bad)
			}
			_, err := d.ToPayload("probe")
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("error %v, want invalid_argument", err)
			}
		})
	}
}

func TestDataConverterInvalidGoTenantFallsThroughToWorkflowTenant(t *testing.T) {
	d := &dataConverter{
		ctx:   core.WithTenant(context.Background(), "acme/eu"),
		wfCtx: sdkworkflow.WithValue(nil, tenantWorkflowKey{}, core.TenantID("beta")),
	}
	tid, _, err := d.tenantBase()
	if err != nil {
		t.Fatal(err)
	}
	if tid != "beta" {
		t.Fatalf("tenant %q want beta", tid)
	}
}

func TestDataConverterValidTenantWorksInBothPaths(t *testing.T) {
	codec, err := newPayloadCodec(testSecretsCodec(t), testBlobStore(t), DefaultClaimCheckBytes)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := newDataConverter(codec, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := dc.(*dataConverter)

	client := d.WithContext(core.WithTenant(context.Background(), "acme")).(*dataConverter)
	if tid, _, err := client.tenantBase(); err != nil || tid != "acme" {
		t.Fatalf("client tenant %q err %v", tid, err)
	}
	if _, err := client.ToPayload("client-value"); err != nil {
		t.Fatalf("client payload: %v", err)
	}

	wf := d.WithWorkflowContext(sdkworkflow.WithValue(nil, tenantWorkflowKey{}, core.TenantID("beta"))).(*dataConverter)
	if tid, _, err := wf.tenantBase(); err != nil || tid != "beta" {
		t.Fatalf("workflow tenant %q err %v", tid, err)
	}
	if _, err := wf.ToPayload("workflow-value"); err != nil {
		t.Fatalf("workflow payload: %v", err)
	}
}
