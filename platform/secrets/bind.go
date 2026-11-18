package secrets

import (
	"crypto/subtle"

	"google.golang.org/protobuf/encoding/protowire"
)

// FrameBoundPlaintext prefixes the tenant length (uvarint) and tenant bytes
// before plaintext for master-key wraps that take no associated data.
func FrameBoundPlaintext(tenant string, plaintext []byte) ([]byte, error) {
	if err := ValidateTenant(tenant); err != nil {
		return nil, err
	}
	var buf []byte
	buf = protowire.AppendVarint(buf, uint64(len(tenant)))
	buf = append(buf, tenant...)
	buf = append(buf, plaintext...)
	return buf, nil
}

// ParseBoundPlaintext checks tenant binding and returns the inner plaintext.
func ParseBoundPlaintext(expectTenant string, inner []byte) ([]byte, error) {
	if err := ValidateTenant(expectTenant); err != nil {
		return nil, err
	}
	n, off := protowire.ConsumeVarint(inner)
	if off < 0 || n > uint64(maxTenantLen) {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	if len(inner) < off+int(n) {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	bound := string(inner[off : off+int(n)])
	if subtle.ConstantTimeCompare([]byte(bound), []byte(expectTenant)) != 1 {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	return append([]byte(nil), inner[off+int(n):]...), nil
}
