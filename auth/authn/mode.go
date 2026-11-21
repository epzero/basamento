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
	// ModeNone trusts every caller on the local machine. Until request
	// authentication is wired into kit serve, every mode listens on loopback
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

// ValidateListen reports invalid_argument when addr is not a loopback listener.
// Request authentication is not wired into kit serve yet, so every mode
// accepts loopback addresses only; non-loopback none keeps the REQ-AUTH-001
// message, while local and oidc report that request auth is not wired.
//
// A host name other than "localhost" is not loopback: it may resolve anywhere,
// and the process cannot prove otherwise.
func ValidateListen(mode Mode, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("listen.addr %q is not a host:port address", addr), map[string]any{"key": "listen.addr"})
	}
	if isLoopbackHost(host) {
		return nil
	}
	if mode == ModeNone {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("auth.mode %q refuses listen.addr %q: auth is required for non-loopback addresses", mode, addr),
			map[string]any{"key": "listen.addr"})
	}
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("listen.addr %q is refused: request authentication is not wired into kit serve yet, so only loopback addresses are accepted", addr),
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
