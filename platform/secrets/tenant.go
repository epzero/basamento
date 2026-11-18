package secrets

import "strings"

// EP0-52 will share tenant grammar in core; until then this package enforces it.
const maxTenantLen = 128

const msgTenantInvalid = "tenant id is not valid"

// ValidateTenant rejects ids that cannot be bound safely in crypto framing.
func ValidateTenant(tenant string) error {
	if tenant == "" {
		return errInvalidArgument(msgTenantRequired)
	}
	if len(tenant) > maxTenantLen {
		return errInvalidArgument(msgTenantInvalid)
	}
	for _, r := range tenant {
		if r < 0x20 || r == 0x7f {
			return errInvalidArgument(msgTenantInvalid)
		}
	}
	if strings.Contains(tenant, "..") || strings.ContainsAny(tenant, `/\`) {
		return errInvalidArgument(msgTenantInvalid)
	}
	return nil
}
