package authn

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestParseMode(t *testing.T) {
	for _, want := range []Mode{ModeNone, ModeLocal, ModeOIDC} {
		got, err := ParseMode(string(want))
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", want, err)
		}
		if got != want {
			t.Fatalf("ParseMode(%q) = %q", want, got)
		}
	}
	for _, bad := range []string{"", "None", "password", "oidc "} {
		if _, err := ParseMode(bad); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("ParseMode(%q) code = %s, want invalid_argument", bad, core.CodeOf(err))
		}
	}
}

func TestValidateListenRefusesNonLoopback(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8080",
		"127.0.0.1:0",
		"[::1]:8080",
		"localhost:8080",
		"LOCALHOST:8080",
	}
	nonLoopback := []string{
		"0.0.0.0:8080",
		"[::]:8080",
		"192.168.1.10:8080",
		"example.test:8080",
		":8080",
	}
	for _, mode := range []Mode{ModeNone, ModeLocal, ModeOIDC} {
		for _, addr := range loopback {
			if err := ValidateListen(mode, addr); err != nil {
				t.Errorf("ValidateListen(%q, %q) = %v, want nil", mode, addr, err)
			}
		}
		for _, addr := range nonLoopback {
			err := ValidateListen(mode, addr)
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Errorf("ValidateListen(%q, %q) code = %s, want invalid_argument", mode, addr, core.CodeOf(err))
				continue
			}
			switch mode {
			case ModeNone:
				if !strings.Contains(err.Error(), "auth is required for non-loopback addresses") {
					t.Errorf("ValidateListen(none, %q) = %q, want the loopback explanation", addr, err)
				}
			default:
				if !strings.Contains(err.Error(), "request authentication is not wired") {
					t.Errorf("ValidateListen(%q, %q) = %q, want request-auth explanation", mode, addr, err)
				}
			}
		}
	}
}

func TestValidateListenRejectsMalformedAddress(t *testing.T) {
	if err := ValidateListen(ModeNone, "not-an-address"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", core.CodeOf(err))
	}
}
