package secrets

import (
	"context"
	"errors"

	"github.com/epzero/basamento/core"
	"gocloud.dev/gcerrors"
)

const (
	msgPayloadCannotDecrypt   = "payload cannot be decrypted"
	msgPayloadNotEncrypted    = "payload is not encrypted"
	msgTenantRequired         = "tenant is required"
	msgTenantNoKeyset         = "tenant has no keyset"
	msgKeysetCannotUnwrap     = "keyset of the tenant cannot be unwrapped"
	msgValueCannotDecrypt     = "value cannot be decrypted"
	msgKeyServiceRefused      = "key service refused the request"
	msgKeysetRevisionConflict = "keyset was changed by another writer"
)

func errInvalidArgument(msg string) error {
	return core.NewError(core.CodeInvalidArgument, msg, nil)
}

func errNotFound(msg string) error {
	return core.NewError(core.CodeNotFound, msg, nil)
}

func errInternal(msg string) error {
	return core.NewError(core.CodeInternal, msg, nil)
}

func errConflict(msg string) error {
	return &core.Error{Code: core.CodeConflict, Message: msg, Retryable: true}
}

func errUnavailableRetry(scheme string) error {
	return &core.Error{Code: core.CodeUnavailable, Message: scheme, Retryable: true}
}

type keeperOp int

const (
	opUnwrapKeyset keeperOp = iota
	opWrapKeyset
	opEncryptValue
	opDecryptValue
)

func classifyKeeperErr(ctx context.Context, err error, scheme string, op keeperOp) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return &core.Error{Code: core.CodeCancelled, Message: "cancelled", Cause: ctx.Err()}
		}
		return &core.Error{Code: core.CodeTimeout, Message: "timeout", Cause: ctx.Err(), Retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return &core.Error{Code: core.CodeCancelled, Message: "cancelled", Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.Error{Code: core.CodeTimeout, Message: "timeout", Cause: err, Retryable: true}
	}
	switch gcerrors.Code(err) {
	case gcerrors.InvalidArgument, gcerrors.FailedPrecondition:
		if op == opDecryptValue {
			return errInvalidArgument(msgValueCannotDecrypt)
		}
		return errInternal(msgKeysetCannotUnwrap)
	case gcerrors.PermissionDenied, gcerrors.NotFound, gcerrors.Unimplemented, gcerrors.AlreadyExists:
		if op == opUnwrapKeyset {
			return errInternal(msgKeysetCannotUnwrap)
		}
		return errInternal(msgKeyServiceRefused)
	default:
		if op == opDecryptValue {
			return errInvalidArgument(msgValueCannotDecrypt)
		}
		return errUnavailableRetry(scheme)
	}
}
