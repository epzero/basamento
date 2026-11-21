package temporal

import (
	"encoding/json"
	"errors"

	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// gateUpdateRecord holds one gate-resolution Update callback outcome for assertions after ExecuteWorkflow.
type gateUpdateRecord struct {
	accepted    bool
	rejected    error
	completed   bool
	completeErr error
}

func sendGateResolveUpdate(env *testsuite.TestWorkflowEnvironment, id, gate string, payload json.RawMessage) *gateUpdateRecord {
	rec := &gateUpdateRecord{}
	env.UpdateWorkflow(ResolveGateUpdate, id, &testsuite.TestUpdateCallback{
		OnAccept: func() { rec.accepted = true },
		OnReject: func(err error) { rec.rejected = err },
		OnComplete: func(_ interface{}, err error) {
			rec.completed = true
			rec.completeErr = err
		},
	}, GateResolution{Gate: gate, Payload: payload})
	return rec
}

func gateUpdateRejectInfo(err error) (code, msg string, ok bool) {
	var ae *sdktemporal.ApplicationError
	if err != nil && errors.As(err, &ae) {
		return ae.Type(), ae.Message(), true
	}
	return "", "", false
}
