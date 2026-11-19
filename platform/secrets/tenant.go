package secrets

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxTenantLen is the maximum byte length of a tenant id (REQ-CORE-005).
const maxTenantLen = 128

const msgTenantInvalid = "tenant id is not valid"

// ValidateTenant mirrors the grammar of REQ-CORE-005, which core.TenantFrom enforces
// for every tenant read from a context; EP0-84 replaces this copy.
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
