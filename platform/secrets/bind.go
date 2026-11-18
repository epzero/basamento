package secrets

import (
	"bytes"
	"crypto/subtle"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	frameKeysetV1 = "basamento.keyset.v1"
	frameValueV1  = "basamento.value.v1"
	canaryTenant  = "__basamento_canary__"
)

// FrameBoundPlaintext builds the keyset-domain plaintext for Wrap (keyset frame).
func FrameBoundPlaintext(tenant string, plaintext []byte) ([]byte, error) {
	if err := ValidateTenant(tenant); err != nil {
		return nil, err
	}
	return frameTagged(frameKeysetV1, tenant, plaintext)
}

// ParseBoundPlaintext unwraps the keyset-domain plaintext after Unwrap.
func ParseBoundPlaintext(expectTenant string, inner []byte) ([]byte, error) {
	return parseTagged(frameKeysetV1, expectTenant, inner)
}

// FrameValuePlaintext builds the value-domain plaintext for Port.Encrypt.
func FrameValuePlaintext(plaintext []byte) []byte {
	var buf []byte
	buf = append(buf, frameValueV1...)
	buf = append(buf, 0)
	buf = append(buf, plaintext...)
	return buf
}

// ParseValuePlaintext unwraps the value-domain plaintext after Decrypt.
func ParseValuePlaintext(inner []byte) ([]byte, error) {
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
	if err := ValidateTenant(expectTenant); err != nil {
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
