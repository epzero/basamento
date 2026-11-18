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
		return &core.Error{Code: core.CodeUnavailable, Message: "blob store unavailable", Retryable: true, Cause: err}
	default:
		if errors.Is(err, os.ErrNotExist) {
			return core.NewError(core.CodeNotFound, "blob not found", nil)
		}
		if strings.Contains(err.Error(), "NoSuchKey") {
			return core.NewError(core.CodeNotFound, "blob not found", nil)
		}
		return &core.Error{Code: core.CodeUnavailable, Message: "blob store error", Retryable: true, Cause: err}
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
