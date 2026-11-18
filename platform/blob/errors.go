package blob

import (
	"errors"
	"os"
	"strings"

	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
)

func mapError(err error) error {
	if err == nil {
		return nil
	}
	code := gcerrors.Code(err)
	switch code {
	case gcerrors.NotFound:
		return core.NewError(core.CodeNotFound, "blob not found", nil)
	case gcerrors.InvalidArgument:
		return core.NewError(core.CodeInvalidArgument, "invalid blob request", nil)
	case gcerrors.PermissionDenied:
		return core.NewError(core.CodePermissionDenied, "blob access denied", nil)
	case gcerrors.Canceled:
		return core.NewError(core.CodeCancelled, "blob operation cancelled", nil)
	case gcerrors.DeadlineExceeded:
		return &core.Error{Code: core.CodeUnavailable, Message: "blob store unavailable", Retryable: true}
	default:
		if errors.Is(err, os.ErrNotExist) {
			return core.NewError(core.CodeNotFound, "blob not found", nil)
		}
		if strings.Contains(err.Error(), "NoSuchKey") {
			return core.NewError(core.CodeNotFound, "blob not found", nil)
		}
		return core.NewError(core.CodeUnavailable, "blob store error", nil)
	}
}

// mapOpenError maps bucket open failures without echoing URL or provider details.
func mapOpenError(err error) error {
	if err == nil {
		return nil
	}
	code := gcerrors.Code(err)
	switch code {
	case gcerrors.NotFound:
		return core.NewError(core.CodeNotFound, "blob not found", nil)
	case gcerrors.InvalidArgument:
		return core.NewError(core.CodeInvalidArgument, "invalid blob location", nil)
	case gcerrors.PermissionDenied:
		return core.NewError(core.CodePermissionDenied, "blob access denied", nil)
	default:
		return core.NewError(core.CodeUnavailable, "blob store unavailable", nil)
	}
}

func unsupportedLocation(u string) error {
	return core.NewError(core.CodeInvalidArgument,
		"unsupported blob location; supported schemes: file, s3, gs, azblob",
		map[string]any{"url": redactURL(u)})
}

func redactURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		return u[:i+3] + "…"
	}
	return "…"
}
