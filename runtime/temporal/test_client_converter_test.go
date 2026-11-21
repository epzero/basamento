package temporal

import (
	"context"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
)

// clientConverter models a Temporal client that always encodes with the tenant in ctx.
// base holds the unbound converter from construction; WithContext/WithWorkflowContext
// always rebind from base, never from a workflow-bound inner, so the SDK test
// environment does not pass a workflow deadlock detector into activity encodes
// (real workers use an unbound converter on the activity side).
type clientConverter struct {
	base       converter.DataConverter
	inner      converter.DataConverter
	tenant     core.TenantID
	inWorkflow bool
}

func (c *clientConverter) baseConverter() converter.DataConverter {
	if c.base != nil {
		return c.base
	}
	return c.inner
}

func (c *clientConverter) aware() interface {
	WithContext(context.Context) converter.DataConverter
	WithWorkflowContext(workflow.Context) converter.DataConverter
} {
	a, _ := c.inner.(interface {
		WithContext(context.Context) converter.DataConverter
		WithWorkflowContext(workflow.Context) converter.DataConverter
	})
	return a
}

func (c *clientConverter) awareOf(dc converter.DataConverter) interface {
	WithContext(context.Context) converter.DataConverter
	WithWorkflowContext(workflow.Context) converter.DataConverter
} {
	a, _ := dc.(interface {
		WithContext(context.Context) converter.DataConverter
		WithWorkflowContext(workflow.Context) converter.DataConverter
	})
	return a
}

func (c *clientConverter) WithContext(ctx context.Context) converter.DataConverter {
	out := *c
	base := c.baseConverter()
	out.base = base
	if a := c.awareOf(base); a != nil {
		out.inner = a.WithContext(ctx)
	}
	return &out
}

func (c *clientConverter) WithWorkflowContext(ctx workflow.Context) converter.DataConverter {
	out := *c
	out.inWorkflow = true
	base := c.baseConverter()
	out.base = base
	if a := c.awareOf(base); a != nil {
		out.inner = a.WithWorkflowContext(ctx)
	}
	return &out
}

func (c *clientConverter) delegate() converter.DataConverter {
	if a := c.aware(); a != nil && c.tenant != "" && !c.inWorkflow {
		return a.WithContext(core.WithTenant(context.Background(), c.tenant))
	}
	return c.inner
}

func (c *clientConverter) ToPayload(value interface{}) (*commonpb.Payload, error) {
	return c.delegate().ToPayload(value)
}
func (c *clientConverter) FromPayload(payload *commonpb.Payload, valuePtr interface{}) error {
	return c.delegate().FromPayload(payload, valuePtr)
}
func (c *clientConverter) ToPayloads(value ...interface{}) (*commonpb.Payloads, error) {
	return c.delegate().ToPayloads(value...)
}
func (c *clientConverter) FromPayloads(payloads *commonpb.Payloads, valuePtrs ...interface{}) error {
	return c.delegate().FromPayloads(payloads, valuePtrs...)
}
func (c *clientConverter) ToString(payload *commonpb.Payload) string {
	return c.delegate().ToString(payload)
}
func (c *clientConverter) ToStrings(payloads *commonpb.Payloads) []string {
	return c.delegate().ToStrings(payloads)
}
