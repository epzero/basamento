package credentials

import (
	"strings"
)

const (
	referencePrefix = "cred://"
	maxNameLen      = 128
)

// validName reports whether name satisfies the credential name grammar of
// REQ-CRED-006: 1 to 128 characters from A-Z, a-z, 0-9, ".", "_", "-" and "/",
// whose "/"-separated segments are each non-empty and neither "." nor "..".
// Names are case-sensitive and used verbatim; there is no normalization.
func validName(name string) bool {
	if name == "" || len(name) > maxNameLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !nameByteOK(name[i]) {
			return false
		}
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func nameByteOK(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '.', c == '_', c == '-', c == '/':
		return true
	default:
		return false
	}
}

// referenceName returns the name of a reference of the form "cred://" followed
// by a valid credential name (REQ-CRED-008). It reports false for a malformed
// reference; callers must not echo the rejected reference.
func referenceName(ref string) (string, bool) {
	if !strings.HasPrefix(ref, referencePrefix) {
		return "", false
	}
	name := ref[len(referencePrefix):]
	if !validName(name) {
		return "", false
	}
	return name, true
}
