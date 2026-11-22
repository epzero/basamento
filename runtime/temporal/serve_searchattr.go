package temporal

import (
	"context"
	"errors"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/epzero/basamento/core"
)

func (r *Runtime) ensureFlowSearchAttribute(ctx context.Context, cl client.Client) error {
	ns := r.clientNamespace()
	resp, err := cl.OperatorService().ListSearchAttributes(ctx, &operatorservice.ListSearchAttributesRequest{
		Namespace: ns,
	})
	if err != nil {
		return r.clientError("list_search_attributes", err)
	}
	if typ, ok := resp.GetCustomAttributes()[FlowSearchAttribute]; ok {
		if typ == enumspb.INDEXED_VALUE_TYPE_KEYWORD {
			return nil
		}
		return core.NewError(core.CodeConflict, "temporal: search attribute BasamentoFlow exists with another type", nil)
	}
	_, err = cl.OperatorService().AddSearchAttributes(ctx, &operatorservice.AddSearchAttributesRequest{
		Namespace: ns,
		SearchAttributes: map[string]enumspb.IndexedValueType{
			FlowSearchAttribute: enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})
	if err == nil {
		return nil
	}
	var exists *serviceerror.AlreadyExists
	if errors.As(err, &exists) {
		return nil
	}
	var denied *serviceerror.PermissionDenied
	if errors.As(err, &denied) {
		return core.NewError(core.CodePermissionDenied,
			"temporal: cannot register the BasamentoFlow search attribute; register it once as a Keyword attribute", nil)
	}
	return r.clientError("add_search_attributes", err)
}
