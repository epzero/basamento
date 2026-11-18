package secrets

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateTenant mirrors REQ-CORE-005 until core ships its own (EP0-52 / PR #60).
const maxTenantLen = 128

const msgTenantInvalid = "tenant id is not valid"

// ValidateTenant enforces the tenant id grammar of REQ-CORE-005.
func ValidateTenant(tenant string) error {
	if tenant == "" {
		return errInvalidArgument(msgTenantRequired)
	}
	if len(tenant) > maxTenantLen {
		return errInvalidArgument(msgTenantInvalid)
	}
	if !utf8.ValidString(tenant) {
		return errInvalidArgument(msgTenantInvalid)
	}
	if tenant == "." || tenant == ".." {
		return errInvalidArgument(msgTenantInvalid)
	}
	if strings.ContainsAny(tenant, `/\`) {
		return errInvalidArgument(msgTenantInvalid)
	}
	for _, r := range tenant {
		if r < 0x20 || r == 0x7f {
			return errInvalidArgument(msgTenantInvalid)
		}
	}
	runes := []rune(tenant)
	if unicode.IsSpace(runes[0]) || unicode.IsSpace(runes[len(runes)-1]) {
		return errInvalidArgument(msgTenantInvalid)
	}
	return nil
}
