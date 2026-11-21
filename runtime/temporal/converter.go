package temporal

import (
	"context"
	"log/slog"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

// dataConverter wraps payloadCodec as a Temporal DataConverter shared by
// workflow and activity workers (REQ-RT-028 … REQ-RT-033).
type dataConverter struct {
	codec  *payloadCodec
	inner  converter.DataConverter
	logger *slog.Logger
	ctx    context.Context
	wfCtx  workflow.Context
}

func newDataConverter(codec *payloadCodec, logger *slog.Logger) (converter.DataConverter, error) {
	if codec == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: data converter needs a codec", nil)
	}
	return &dataConverter{
		codec:  codec,
		inner:  converter.GetDefaultDataConverter(),
		logger: logger,
	}, nil
}

// WithContext implements converter.ContextAware for the Go client and activities.
func (d *dataConverter) WithContext(ctx context.Context) converter.DataConverter {
	clone := *d
	clone.ctx = ctx
	clone.wfCtx = nil
	return &clone
}

// WithWorkflowContext implements converter.ContextAware for workflows.
func (d *dataConverter) WithWorkflowContext(ctx workflow.Context) converter.DataConverter {
	clone := *d
	clone.wfCtx = ctx
	return &clone
}

func (d *dataConverter) workflowBound() bool {
	return d.wfCtx != nil
}

func (d *dataConverter) tenantBase() (core.TenantID, context.Context, error) {
	if d.ctx != nil {
		if tid, ok := core.TenantFrom(d.ctx); ok {
			return tid, d.ctx, nil
		}
	}
	if d.wfCtx != nil {
		if v := d.wfCtx.Value(tenantWorkflowKey{}); v != nil {
			if id, ok := v.(core.TenantID); ok && id != "" {
				return id, core.WithTenant(context.Background(), id), nil
			}
		}
	}
	return "", nil, core.NewError(core.CodeInvalidArgument, "temporal: no tenant in context", nil)
}

func (d *dataConverter) codecCtx() (context.Context, func(), error) {
	_, base, err := d.tenantBase()
	if err != nil {
		return nil, nil, err
	}
	if !d.workflowBound() {
		return base, func() {}, nil
	}
	ctx, cancel := context.WithTimeout(base, workflowCodecTimeout)
	return ctx, cancel, nil
}

func (d *dataConverter) logWorkflowCodec(pe *core.Error) {
	logger := d.logger
	if logger == nil {
		logger = slog.Default()
	}
	tid, _, _ := d.tenantBase()
	wfID := ""
	if d.wfCtx != nil {
		wfID = workflow.GetInfo(d.wfCtx).WorkflowExecution.ID
	}
	logger.Error("temporal: payload codec failed in workflow",
		slog.String("tenant", string(tid)),
		slog.String("workflow", wfID),
		slog.String("code", string(pe.Code)),
		slog.String("error", truncateLogTextRunes(pe.Message)),
	)
}

func (d *dataConverter) ToPayload(value interface{}) (*commonpb.Payload, error) {
	ctx, cancel, err := d.codecCtx()
	if err != nil {
		return nil, err
	}
	defer cancel()
	plain, err := d.inner.ToPayload(value)
	if err != nil {
		return nil, err
	}
	enc, err := d.codec.Encode(ctx, payloadFromProto(plain))
	if err = workflowCodecPolicy(d.workflowBound(), d.logWorkflowCodec, err); err != nil {
		return nil, err
	}
	return payloadToProto(enc), nil
}

func (d *dataConverter) FromPayload(payload *commonpb.Payload, valuePtr interface{}) error {
	ctx, cancel, err := d.codecCtx()
	if err != nil {
		return err
	}
	defer cancel()
	plain, err := d.codec.Decode(ctx, payloadFromProto(payload))
	if err = workflowCodecPolicy(d.workflowBound(), d.logWorkflowCodec, err); err != nil {
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
