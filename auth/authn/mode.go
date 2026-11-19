package authn

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/epzero/basamento/core"
)

// Mode is an authentication mode (ADR-0008).
type Mode string

const (
	// ModeNone trusts every caller on the local machine and listens on loopback
	// only (REQ-AUTH-001).
	ModeNone Mode = "none"
	// ModeLocal authenticates in-app accounts and scoped API tokens.
	ModeLocal Mode = "local"
	// ModeOIDC authenticates JWTs from the configured issuer (REQ-AUTH-003).
	ModeOIDC Mode = "oidc"
)

// ParseMode returns the mode named s. It reports invalid_argument for any
// other value and never echoes the input, which may come from configuration.
func ParseMode(s string) (Mode, error) {
	switch mode := Mode(s); mode {
	case ModeNone, ModeLocal, ModeOIDC:
		return mode, nil
	}
	return "", core.NewError(core.CodeInvalidArgument,
		`auth.mode must be one of "none", "local" or "oidc"`, map[string]any{"key": "auth.mode"})
}

// ValidateListen reports invalid_argument when mode is none and addr is not a
// loopback listener. The none mode has no way to tell callers apart, so a
// non-loopback address would expose every tenant to the network (REQ-AUTH-001).
//
// A host name other than "localhost" is not loopback: it may resolve anywhere,
// and the process cannot prove otherwise.
func ValidateListen(mode Mode, addr string) error {
	if mode != ModeNone {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("listen.addr %q is not a host:port address", addr), map[string]any{"key": "listen.addr"})
	}
	if isLoopbackHost(host) {
		return nil
	}
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("auth.mode %q refuses listen.addr %q: auth is required for non-loopback addresses", mode, addr),
		map[string]any{"key": "listen.addr"})
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}
