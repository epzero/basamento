package secrets

import "github.com/epzero/basamento/core"

const (
	msgPayloadCannotDecrypt = "payload cannot be decrypted"
	msgPayloadNotEncrypted  = "payload is not encrypted"
	msgTenantRequired       = "tenant is required"
	msgKeysetCannotUnwrap   = "keyset of the tenant cannot be unwrapped"
	msgValueCannotDecrypt   = "value cannot be decrypted"
)

func errInvalidArgument(msg string) error {
	return core.NewError(core.CodeInvalidArgument, msg, nil)
}

func errInternal(msg string) error {
	return core.NewError(core.CodeInternal, msg, nil)
}

func errUnavailable(scheme string) error {
	return core.NewError(core.CodeUnavailable, scheme, nil)
}
