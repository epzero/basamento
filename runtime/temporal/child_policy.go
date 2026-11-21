package temporal

import (
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/epzero/basamento/core"
)

const defaultParentClose = "terminate"

func parentClosePolicy(parentClose string) (enumspb.ParentClosePolicy, error) {
	policy := parentClose
	if policy == "" {
		policy = defaultParentClose
	}
	switch policy {
	case "terminate":
		return enumspb.PARENT_CLOSE_POLICY_TERMINATE, nil
	case "request_cancel":
		return enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL, nil
	case "abandon":
		return enumspb.PARENT_CLOSE_POLICY_ABANDON, nil
	default:
		return 0, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("temporal: unknown parent close policy %q", policy),
			map[string]any{"policy": policy})
	}
}
