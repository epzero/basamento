package temporal

import (
	"context"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

// dataConverter wraps payloadCodec as a Temporal DataConverter shared by
// workflow and activity workers (REQ-RT-028 … REQ-RT-033).
type dataConverter struct {
	codec *payloadCodec
	inner converter.DataConverter
	ctx   context.Context
	wfCtx workflow.Context
}

func newDataConverter(codec *payloadCodec) (converter.DataConverter, error) {
	if codec == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: data converter needs a codec", nil)
	}
	return &dataConverter{
		codec: codec,
		inner: converter.GetDefaultDataConverter(),
	}, nil
}

// WithContext implements converter.ContextAware for the Go client and activities.
func (d *dataConverter) WithContext(ctx context.Context) converter.DataConverter {
	clone := *d
	clone.ctx = ctx
	return &clone
}

// WithWorkflowContext implements converter.ContextAware for workflows.
func (d *dataConverter) WithWorkflowContext(ctx workflow.Context) converter.DataConverter {
	clone := *d
	clone.wfCtx = ctx
	return &clone
}

func (d *dataConverter) tenantCtx() (context.Context, error) {
	if d.ctx != nil {
		if _, ok := core.TenantFrom(d.ctx); ok {
			return d.ctx, nil
		}
	}
	if d.wfCtx != nil {
		if v := d.wfCtx.Value(tenantWorkflowKey{}); v != nil {
			if id, ok := v.(core.TenantID); ok && id != "" {
				return core.WithTenant(context.Background(), id), nil
			}
		}
	}
	return nil, core.NewError(core.CodeInvalidArgument, "temporal: no tenant in context", nil)
}

func (d *dataConverter) ToPayload(value interface{}) (*commonpb.Payload, error) {
	ctx, err := d.tenantCtx()
	if err != nil {
		return nil, err
	}
	plain, err := d.inner.ToPayload(value)
	if err != nil {
		return nil, err
	}
	enc, err := d.codec.Encode(ctx, payloadFromProto(plain))
	if err != nil {
		return nil, err
	}
	return payloadToProto(enc), nil
}

func (d *dataConverter) FromPayload(payload *commonpb.Payload, valuePtr interface{}) error {
	ctx, err := d.tenantCtx()
	if err != nil {
		return err
	}
	plain, err := d.codec.Decode(ctx, payloadFromProto(payload))
	if err != nil {
		return err
	}
	return d.inner.FromPayload(payloadToProto(plain), valuePtr)
}

func (d *dataConverter) ToPayloads(value ...interface{}) (*commonpb.Payloads, error) {
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

func (d *dataConverter) FromPayloads(payloads *commonpb.Payloads, valuePtrs ...interface{}) error {
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

func (d *dataConverter) ToString(payload *commonpb.Payload) string {
	return d.inner.ToString(payload)
}

func (d *dataConverter) ToStrings(payloads *commonpb.Payloads) []string {
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
