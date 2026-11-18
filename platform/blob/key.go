package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/epzero/basamento/core"
)

const keyPrefix = "sha256:"

var contentKeyRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ContentKeyOf returns the content key for literal bytes.
func ContentKeyOf(data []byte) string {
	sum := sha256.Sum256(data)
	return keyPrefix + hex.EncodeToString(sum[:])
}

// ContentKeyOfReader hashes all of r and returns its content key.
func ContentKeyOfReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return keyPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// ValidateContentKey reports whether k is a well-formed content key.
func ValidateContentKey(k string) bool {
	if !contentKeyRE.MatchString(k) {
		return false
	}
	if strings.ContainsAny(k, "/\\") {
		return false
	}
	return true
}

// HexOf returns the 64 lowercase hex digits of k, or an error.
func HexOf(k string) (string, error) {
	if !ValidateContentKey(k) {
		return "", core.NewError(core.CodeInvalidArgument, "malformed content key", nil)
	}
	return k[len(keyPrefix):], nil
}

// objectPath is the bucket key for tenant tid and content key k.
func objectPath(tid core.TenantID, k string) (string, error) {
	hex, err := HexOf(k)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", tid, hex), nil
}

// metaPath stores the content type beside the blob object.
func metaPath(tid core.TenantID, k string) (string, error) {
	hex, err := HexOf(k)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s.meta", tid, hex), nil
}
