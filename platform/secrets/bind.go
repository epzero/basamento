package secrets

import (
	"bytes"
	"context"
	"crypto/subtle"

	"github.com/epzero/basamento/core"
	"google.golang.org/protobuf/encoding/protowire"
)

// maxTenantLen caps the protobuf length prefix when a keyset frame is parsed so
// int(n) cannot overflow; a longer prefix is refused early. It is not a grammar check.
const maxTenantLen = 128

// tenantStringOK checks tenant against REQ-CORE-005 via core.TenantFrom.
func tenantStringOK(tenant string) error {
	if tenant == "" {
		return errInvalidArgument(msgTenantRequired)
	}
	if _, ok := core.TenantFrom(core.WithTenant(context.Background(), core.TenantID(tenant))); !ok {
		return errInvalidArgument(msgTenantInvalid)
	}
	return nil
}

const (
	frameKeysetV1 = "basamento.keyset.v1"
	frameValueV1  = "basamento.value.v1"
	canaryTenant  = "__basamento_canary__"
)

// FrameBoundPlaintext builds the keyset-domain plaintext for Wrap (keyset frame).
func FrameBoundPlaintext(tenant string, plaintext []byte) ([]byte, error) {
	if err := tenantStringOK(tenant); err != nil {
		return nil, err
	}
	return frameTagged(frameKeysetV1, tenant, plaintext)
}

// ParseBoundPlaintext unwraps the keyset-domain plaintext after Unwrap.
func ParseBoundPlaintext(expectTenant string, inner []byte) ([]byte, error) {
	return parseTagged(frameKeysetV1, expectTenant, inner)
}

// frameValuePlaintext tags plaintext for Port.Encrypt (value domain).
func frameValuePlaintext(plaintext []byte) []byte {
	var buf []byte
	buf = append(buf, frameValueV1...)
	buf = append(buf, 0)
	buf = append(buf, plaintext...)
	return buf
}

// parseValuePlaintext unwraps the value-domain plaintext after Decrypt.
func parseValuePlaintext(inner []byte) ([]byte, error) {
	if len(inner) < len(frameValueV1)+1 {
		return nil, errInvalidArgument(msgValueCannotDecrypt)
	}
	if !bytes.Equal(inner[:len(frameValueV1)], []byte(frameValueV1)) || inner[len(frameValueV1)] != 0 {
		return nil, errInvalidArgument(msgValueCannotDecrypt)
	}
	return append([]byte(nil), inner[len(frameValueV1)+1:]...), nil
}

func frameTagged(tag, tenant string, plaintext []byte) ([]byte, error) {
	var buf []byte
	buf = append(buf, tag...)
	buf = append(buf, 0)
	buf = protowire.AppendVarint(buf, uint64(len(tenant)))
	buf = append(buf, tenant...)
	buf = append(buf, plaintext...)
	return buf, nil
}

func parseTagged(tag, expectTenant string, inner []byte) ([]byte, error) {
	if err := tenantStringOK(expectTenant); err != nil {
		return nil, err
	}
	tagLen := len(tag)
	if len(inner) < tagLen+1 {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	if !bytes.Equal(inner[:tagLen], []byte(tag)) || inner[tagLen] != 0 {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	inner = inner[tagLen+1:]
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
