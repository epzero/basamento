package temporal

import (
	"context"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

// DataConverter wraps Codec as a Temporal DataConverter shared by workflow and
// activity workers (REQ-RT-028 … REQ-RT-033).
type DataConverter struct {
	codec          *Codec
	inner          converter.DataConverter
	ctx            context.Context
	wfCtx          workflow.Context
	fallbackTenant core.TenantID
	observe        func(Encoded)
	// FaultReplace, when set, may swap the payload written to history (BDD faults).
	FaultReplace func(Encoded, *commonpb.Payload) *commonpb.Payload
}

// NewDataConverter returns a context-aware data converter. observe, when non-nil,
// is called after every successful Encode with the encoded form (for harness
// recorders). A nil codec is refused with invalid_argument.
func NewDataConverter(codec *Codec, observe func(Encoded)) (*DataConverter, error) {
	if codec == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: data converter needs a codec", nil)
	}
	return &DataConverter{
		codec:   codec,
		inner:   converter.GetDefaultDataConverter(),
		observe: observe,
	}, nil
}

// WithContext implements converter.ContextAware for the Go client and activities.
func (d *DataConverter) WithContext(ctx context.Context) converter.DataConverter {
	clone := *d
	clone.ctx = ctx
	return &clone
}

// WithWorkflowContext implements converter.ContextAware for workflows.
func (d *DataConverter) WithWorkflowContext(ctx workflow.Context) converter.DataConverter {
	clone := *d
	clone.wfCtx = ctx
	return &clone
}

// WithFallbackTenant pins a tenant for in-process test environments that do not
// propagate headers on workflow result decode.
func (d *DataConverter) WithFallbackTenant(t core.TenantID) *DataConverter {
	clone := *d
	clone.fallbackTenant = t
	return &clone
}

func (d *DataConverter) tenantCtx() context.Context {
	if d.ctx != nil {
		if _, ok := core.TenantFrom(d.ctx); ok {
			return d.ctx
		}
	}
	if d.wfCtx != nil {
		if v := d.wfCtx.Value(tenantWorkflowKey{}); v != nil {
			if id, ok := v.(core.TenantID); ok && id != "" {
				return core.WithTenant(context.Background(), id)
			}
		}
	}
	if d.fallbackTenant != "" {
		return core.WithTenant(context.Background(), d.fallbackTenant)
	}
	return d.ctx
}

func (d *DataConverter) ToPayload(value interface{}) (*commonpb.Payload, error) {
	ctx := d.tenantCtx()
	if ctx == nil {
		if in, ok := value.(HostInput); ok && in.Tenant != "" {
			ctx = core.WithTenant(context.Background(), in.Tenant)
		}
	}
	if ctx == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: encode without a tenant", nil)
	}
	plain, err := d.inner.ToPayload(value)
	if err != nil {
		return nil, err
	}
	enc, err := d.codec.Encode(ctx, payloadFromProto(plain))
	if err != nil {
		return nil, err
	}
	if d.observe != nil {
		d.observe(enc)
	}
	out := payloadToProto(enc.Payload)
	if d.FaultReplace != nil {
		if replaced := d.FaultReplace(enc, out); replaced != nil {
			out = replaced
		}
	}
	return out, nil
}

func (d *DataConverter) FromPayload(payload *commonpb.Payload, valuePtr interface{}) error {
	ctx := d.tenantCtx()
	if ctx == nil {
		return core.NewError(core.CodeInvalidArgument, "temporal: decode without a tenant", nil)
	}
	plain, err := d.codec.Decode(ctx, payloadFromProto(payload))
	if err != nil {
		return err
	}
	return d.inner.FromPayload(payloadToProto(plain), valuePtr)
}

func (d *DataConverter) ToPayloads(value ...interface{}) (*commonpb.Payloads, error) {
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

func (d *DataConverter) FromPayloads(payloads *commonpb.Payloads, valuePtrs ...interface{}) error {
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

func (d *DataConverter) ToString(payload *commonpb.Payload) string {
	return d.inner.ToString(payload)
}

func (d *DataConverter) ToStrings(payloads *commonpb.Payloads) []string {
	return d.inner.ToStrings(payloads)
}

func payloadFromProto(p *commonpb.Payload) secrets.Payload {
	if p == nil {
		return secrets.Payload{}
	}
	md := make(map[string][]byte, len(p.Metadata))
	for k, v := range p.Metadata {
		md[k] = append([]byte(nil), v...)
	}
	return secrets.Payload{Metadata: md, Data: append([]byte(nil), p.Data...)}
}

func payloadToProto(p secrets.Payload) *commonpb.Payload {
	md := make(map[string][]byte, len(p.Metadata))
	for k, v := range p.Metadata {
		md[k] = append([]byte(nil), v...)
	}
	return &commonpb.Payload{Metadata: md, Data: append([]byte(nil), p.Data...)}
}
