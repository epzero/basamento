package api

import (
	"encoding/json"
	"net/http"

	"github.com/epzero/basamento/core"
)

// errorBody is the response body of every failed request. Only the stable code
// is returned: the platform's English messages are for logs, and no field
// echoes a request value (a message would otherwise risk echoing a tenant name or a
// credential to a caller that guessed wrong).
type errorBody struct {
	Error struct {
		Code core.Code `json:"code"`
	} `json:"error"`
}

// statusFor maps the platform's closed error taxonomy to HTTP status codes.
// The mapping is part of the API contract: a client switches on the body's
// code, and the status is the transport's summary of it.
func statusFor(code core.Code) int {
	switch code {
	case core.CodeInvalidArgument:
		return http.StatusBadRequest
	case core.CodeUnauthenticated:
		return http.StatusUnauthorized
	case core.CodePermissionDenied:
		return http.StatusForbidden
	case core.CodeNotFound:
		return http.StatusNotFound
	case core.CodeConflict:
		return http.StatusConflict
	case core.CodeBudgetExceeded:
		return http.StatusPaymentRequired
	case core.CodeRateLimited:
		return http.StatusTooManyRequests
	case core.CodeTimeout:
		return http.StatusGatewayTimeout
	case core.CodeUnavailable:
		return http.StatusServiceUnavailable
	case core.CodeUnsupported:
		return http.StatusNotImplemented
	case core.CodeVendor:
		return http.StatusBadGateway
	case core.CodeCancelled:
		return http.StatusConflict
	case core.CodeRecordingMissing:
		return http.StatusNotFound
	case core.CodeAmbiguousStart:
		return http.StatusConflict
	case core.CodeDeterminismBroken, core.CodeInternal:
		return http.StatusInternalServerError
	}
	return http.StatusInternalServerError
}

// writeError answers err with its platform code and the matching status. An
// error without a platform code is internal, never the error text.
func writeError(w http.ResponseWriter, err error) {
	code := core.CodeOf(err)
	body := errorBody{}
	body.Error.Code = code
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusFor(code))
	_ = json.NewEncoder(w).Encode(body)
}

// writeJSON answers status with v encoded as JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
