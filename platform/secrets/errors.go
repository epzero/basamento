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
	msgTenantInvalid          = "tenant id is not valid"
	msgTenantNoKeyset         = "tenant has no keyset"
	msgKeysetCannotUnwrap     = "keyset of the tenant cannot be unwrapped"
	msgKeysetCannotWrap       = "keyset of the tenant cannot be wrapped"
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

// classifyCtxErr maps context cancellation or deadline errors; ok when handled.
func classifyCtxErr(ctx context.Context, err error) (error, bool) {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return &core.Error{Code: core.CodeCancelled, Message: "cancelled", Cause: ctx.Err()}, true
		}
		return &core.Error{Code: core.CodeTimeout, Message: "timeout", Cause: ctx.Err(), Retryable: true}, true
	}
	if errors.Is(err, context.Canceled) {
		return &core.Error{Code: core.CodeCancelled, Message: "cancelled", Cause: err}, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.Error{Code: core.CodeTimeout, Message: "timeout", Cause: err, Retryable: true}, true
	}
	return nil, false
}

func classifyKeeperErr(ctx context.Context, err error, scheme string, op keeperOp) error {
	if err == nil {
		return nil
	}
	if c, ok := classifyCtxErr(ctx, err); ok {
		return c
	}
	if scheme == "localsecrets" {
		return classifyLocalKeeperErr(op, err)
	}
	return classifyCloudKeeperErr(errorCode(err), scheme, op)
}

func errorCode(err error) gcerrors.ErrorCode {
	type withCode interface{ Code() gcerrors.ErrorCode }
	var wc withCode
	if errors.As(err, &wc) {
		return wc.Code()
	}
	return gcerrors.Code(err)
}

func classifyLocalKeeperErr(op keeperOp, err error) error {
	switch errorCode(err) {
	case gcerrors.InvalidArgument, gcerrors.FailedPrecondition:
		if op == opDecryptValue {
			return errInvalidArgument(msgValueCannotDecrypt)
		}
		if op == opUnwrapKeyset {
			return errInternal(msgKeysetCannotUnwrap)
		}
		return errInternal(msgKeyServiceRefused)
	case gcerrors.PermissionDenied, gcerrors.NotFound, gcerrors.Unimplemented, gcerrors.AlreadyExists:
		if op == opUnwrapKeyset {
			return errInternal(msgKeysetCannotUnwrap)
		}
		return errInternal(msgKeyServiceRefused)
	default:
		if op == opDecryptValue {
			return errInvalidArgument(msgValueCannotDecrypt)
		}
		if op == opUnwrapKeyset {
			return errInternal(msgKeysetCannotUnwrap)
		}
		return errInternal(msgKeyServiceRefused)
	}
}

func classifyCloudKeeperErr(code gcerrors.ErrorCode, scheme string, op keeperOp) error {
	switch code {
	case gcerrors.InvalidArgument, gcerrors.FailedPrecondition:
		switch op {
		case opDecryptValue:
			return errInvalidArgument(msgValueCannotDecrypt)
		case opUnwrapKeyset:
			return errInternal(msgKeysetCannotUnwrap)
		case opWrapKeyset:
			return errInternal(msgKeysetCannotWrap)
		default:
			return errInternal(msgKeyServiceRefused)
		}
	case gcerrors.PermissionDenied, gcerrors.NotFound, gcerrors.Unimplemented, gcerrors.AlreadyExists:
		if op == opUnwrapKeyset {
			return errInternal(msgKeysetCannotUnwrap)
		}
		return errInternal(msgKeyServiceRefused)
	default:
		return errUnavailableRetry(scheme)
	}
}
