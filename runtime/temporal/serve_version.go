package temporal

import (
	"context"
	"strconv"
	"strings"
	"unicode"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/epzero/basamento/core"
)

const (
	supportedServerMajor = 1
	supportedServerMinor = 32
)

// SupportedServerVersion is the pinned major.minor Temporal server release (REQ-RT-037).
const SupportedServerVersion = "1.32"

func (r *Runtime) checkServerVersion(ctx context.Context, cl client.Client) error {
	resp, err := cl.WorkflowService().GetSystemInfo(ctx, &workflowservice.GetSystemInfoRequest{})
	if err != nil {
		return r.clientError("get_system_info", err)
	}
	raw := resp.GetServerVersion()
	if serverVersionSupported(raw) {
		return nil
	}
	return core.NewError(core.CodeUnsupported,
		"temporal: the server version is outside the supported range",
		map[string]any{
			"server_version": truncateVersionDetail(raw),
			"supported":      SupportedServerVersion,
		})
}

// serverVersionSupported reports whether serverVersion matches the pinned major.minor.
func serverVersionSupported(serverVersion string) bool {
	major, minor, ok := parseServerVersionMajorMinor(serverVersion)
	if !ok {
		return false
	}
	return major == supportedServerMajor && minor == supportedServerMinor
}

// parseServerVersionMajorMinor extracts major and minor from a Temporal server version string.
func parseServerVersionMajorMinor(v string) (major, minor int, ok bool) {
	if v == "" {
		return 0, 0, false
	}
	if strings.ContainsFunc(v, unicode.IsSpace) {
		return 0, 0, false
	}
	if len(v) > 0 && v[0] == 'v' {
		v = v[1:]
		if v == "" {
			return 0, 0, false
		}
	}
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		v = v[:idx]
	}
	if v == "" || strings.HasSuffix(v, ".") {
		return 0, 0, false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, ok = parseVersionComponent(parts[0])
	if !ok {
		return 0, 0, false
	}
	minor, ok = parseVersionComponent(parts[1])
	if !ok {
		return 0, 0, false
	}
	return major, minor, true
}

// parseVersionComponent parses a single dotted version component without leading zeros.
func parseVersionComponent(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// truncateVersionDetail shortens a version string for error details.
func truncateVersionDetail(s string) string {
	const maxRunes = 64
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}
