package steps

import (
	"context"
	"encoding/json"
	"sync"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime/temporal"
)

type contextAwareConverter interface {
	converter.DataConverter
	WithContext(context.Context) converter.DataConverter
	WithWorkflowContext(workflow.Context) converter.DataConverter
}

// recordingConverter decorates the production data converter for BDD: it models
// a tenant-aware client when the SDK encodes without a Go context, records history
// payloads, and injects the RT-032/033 faults on activity results (activity.IsActivity
// is true in WithContext for activity encodes). base is the unbound converter from
// construction; WithContext/WithWorkflowContext rebind from base so activity encodes
// never inherit the workflow deadlock detector (SDK lock inversion in test env only).
type recordingConverter struct {
	base          converter.DataConverter
	inner         converter.DataConverter
	enc           *secrets.Codec
	tenant        core.TenantID
	recorded      *[]recordedPayload
	faultMode     func() string
	faultKey      func() string
	faultInjected *bool
	mu            sync.Mutex
	inActivity    bool
	inWorkflow    bool
}

func (d *recordingConverter) baseConverter() converter.DataConverter {
	if d.base != nil {
		return d.base
	}
	return d.inner
}

func (d *recordingConverter) awareOf(dc converter.DataConverter) contextAwareConverter {
	ca, _ := dc.(contextAwareConverter)
	return ca
}

func (d *recordingConverter) aware() contextAwareConverter {
	return d.awareOf(d.inner)
}

func (d *recordingConverter) WithContext(ctx context.Context) converter.DataConverter {
	base := d.baseConverter()
	c := recordingConverter{
		base: base, enc: d.enc, tenant: d.tenant, recorded: d.recorded,
		faultMode: d.faultMode, faultKey: d.faultKey, faultInjected: d.faultInjected,
		inActivity: activity.IsActivity(ctx), inWorkflow: d.inWorkflow,
	}
	if ca := d.awareOf(base); ca != nil {
		c.inner = ca.WithContext(ctx)
	} else {
		c.inner = base
	}
	return &c
}

func (d *recordingConverter) WithWorkflowContext(ctx workflow.Context) converter.DataConverter {
	base := d.baseConverter()
	c := recordingConverter{
		base: base, enc: d.enc, tenant: d.tenant, recorded: d.recorded,
		faultMode: d.faultMode, faultKey: d.faultKey, faultInjected: d.faultInjected,
		inActivity: d.inActivity, inWorkflow: true,
	}
	if ca := d.awareOf(base); ca != nil {
		c.inner = ca.WithWorkflowContext(ctx)
	} else {
		c.inner = base
	}
	return &c
}

func (d *recordingConverter) delegate() converter.DataConverter {
	if ca := d.aware(); ca != nil && d.tenant != "" && !d.inWorkflow {
		// The SDK test environment encodes workflow input and GetWorkflowResult without
		// a Go context; a real client always calls with the tenant in ctx.
		return ca.WithContext(core.WithTenant(context.Background(), d.tenant))
	}
	return d.inner
}

func (d *recordingConverter) ToPayload(value interface{}) (*commonpb.Payload, error) {
	p, err := d.delegate().ToPayload(value)
	if err != nil {
		return nil, err
	}
	d.recordPayload(p)
	if d.inActivity {
		if replaced := d.maybeFault(p); replaced != nil {
			p = replaced
		}
	}
	return p, nil
}

func (d *recordingConverter) FromPayload(payload *commonpb.Payload, valuePtr interface{}) error {
	return d.delegate().FromPayload(payload, valuePtr)
}

func (d *recordingConverter) ToPayloads(value ...interface{}) (*commonpb.Payloads, error) {
	payloads := make([]*commonpb.Payload, len(value))
	for i, v := range value {
		p, err := d.ToPayload(v)
		if err != nil {
			return nil, err
		}
		payloads[i] = p
	}
	return &commonpb.Payloads{Payloads: payloads}, nil
}

func (d *recordingConverter) FromPayloads(payloads *commonpb.Payloads, valuePtrs ...interface{}) error {
	if payloads == nil {
		return nil
	}
	for i, p := range payloads.Payloads {
		if i >= len(valuePtrs) {
			break
		}
		if err := d.FromPayload(p, valuePtrs[i]); err != nil {
			return err
		}
	}
	return nil
}

func (d *recordingConverter) ToString(payload *commonpb.Payload) string {
	return d.delegate().ToString(payload)
}

func (d *recordingConverter) ToStrings(payloads *commonpb.Payloads) []string {
	return d.delegate().ToStrings(payloads)
}

func (d *recordingConverter) recordPayload(p *commonpb.Payload) {
	claim := d.isClaimCheck(p)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.recorded == nil {
		return
	}
	*d.recorded = append(*d.recorded, recordedPayload{
		metadata:   copyMeta(p.Metadata),
		data:       append([]byte(nil), p.Data...),
		claimCheck: claim,
	})
}

func (d *recordingConverter) isClaimCheck(p *commonpb.Payload) bool {
	ctx := core.WithTenant(context.Background(), d.tenant)
	sp := secrets.Payload{Metadata: copyMeta(p.Metadata), Data: append([]byte(nil), p.Data...)}
	if string(sp.Metadata["encoding"]) != secrets.EncodingEncrypted {
		return false
	}
	plain, err := d.enc.Decode(ctx, sp)
	if err != nil {
		return false
	}
	return string(plain.Metadata["encoding"]) == temporal.ClaimCheckEncoding
}

func (d *recordingConverter) maybeFault(p *commonpb.Payload) *commonpb.Payload {
	if d.faultInjected != nil && *d.faultInjected {
		return nil
	}
	mode := ""
	if d.faultMode != nil {
		mode = d.faultMode()
	}
	if mode == "" {
		return nil
	}
	if d.faultInjected != nil {
		*d.faultInjected = true
	}
	switch mode {
	case "plain":
		return &commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(`{"ok":true}`)}
	case "foreign":
		key := ""
		if d.faultKey != nil {
			key = d.faultKey()
		}
		marker, _ := json.Marshal(struct {
			Key  string `json:"key"`
			Size int    `json:"size"`
		}{Key: key, Size: 8192})
		encMarker, err := d.enc.Encode(core.WithTenant(context.Background(), d.tenant), secrets.Payload{
			Metadata: map[string][]byte{"encoding": []byte(temporal.ClaimCheckEncoding)},
			Data:     marker,
		})
		if err != nil {
			return nil
		}
		return &commonpb.Payload{Metadata: encMarker.Metadata, Data: encMarker.Data}
	default:
		return nil
	}
}
