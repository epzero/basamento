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

func TestValidateListenNoneRefusesNonLoopback(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8080",
		"127.0.0.1:0",
		"[::1]:8080",
		"localhost:8080",
		"LOCALHOST:8080",
	}
	for _, addr := range loopback {
		if err := ValidateListen(ModeNone, addr); err != nil {
			t.Errorf("ValidateListen(none, %q) = %v, want nil", addr, err)
		}
	}

	nonLoopback := []string{
		"0.0.0.0:8080",
		"[::]:8080",
		"192.168.1.10:8080",
		"example.test:8080",
		":8080",
	}
	for _, addr := range nonLoopback {
		err := ValidateListen(ModeNone, addr)
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Errorf("ValidateListen(none, %q) code = %s, want invalid_argument", addr, core.CodeOf(err))
			continue
		}
		if !strings.Contains(err.Error(), "auth is required for non-loopback addresses") {
			t.Errorf("ValidateListen(none, %q) = %q, want the loopback explanation", addr, err)
		}
	}
}

func TestValidateListenOtherModesAllowNonLoopback(t *testing.T) {
	for _, mode := range []Mode{ModeLocal, ModeOIDC} {
		for _, addr := range []string{"0.0.0.0:8080", "[::]:8080"} {
			if err := ValidateListen(mode, addr); err != nil {
				t.Errorf("ValidateListen(%q, %q) = %v, want nil", mode, addr, err)
			}
		}
	}
}

func TestValidateListenRejectsMalformedAddress(t *testing.T) {
	if err := ValidateListen(ModeNone, "not-an-address"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", core.CodeOf(err))
	}
}
