package config

import (
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Report renders the effective configuration with its sources, one line per
// key in table order:
//
//	key: value (source)
//
// Credential-bearing parts of a URL setting print as *** (REQ-CONFIG-008):
// userinfo, password and token query parameters, the key material of a
// localsecrets URL, and every part of a URL that cannot be classified. A URL
// setting that cannot be parsed prints as *** in full.
func (r Result) Report(w io.Writer) {
	for _, s := range settings {
		v, ok := r.values[s.key]
		if !ok {
			continue
		}
		if isPortKey(s.key) {
			v = maskedURL(v)
		}
		_, _ = fmt.Fprintf(w, "%s: %s (%s)\n", s.key, v, r.Sources[s.key])
	}
}

const redactedURLMarker = "***"

// credentialQueryKeys are query parameters that may carry a secret. The libpq
// keys are the ones platform/sql already redacts (REQ-SQL-024); the others are
// the token and signature parameters of the bus, secrets and object-store
// URLs this configuration accepts, including the aws_* aliases some S3
// clients use.
var credentialQueryKeys = []string{
	"password",
	"passwd",
	"token",
	"sig",
	"sv",
	"access_key_id",
	"secret_access_key",
	"aws_access_key_id",
	"aws_secret_access_key",
	"session_token",
	"sslpassword",
	"passfile",
	"sslcert",
	"sslkey",
	"sslrootcert",
	"x-amz-signature",
	"x-amz-credential",
	"x-amz-security-token",
	"x-goog-signature",
	"x-goog-credential",
}

// maskedURL redacts the credential-bearing parts of a port URL. It edits the
// raw text so parameters that are not secrets keep their order and escaping,
// and the marker itself is never percent-encoded.
func maskedURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return redactedURLMarker
	}
	i := strings.Index(raw, "://")
	if i < 0 {
		// Opaque form (scheme:opaque). No part of the opaque text is known to
		// be safe, so mask it as a whole.
		return u.Scheme + "://" + redactedURLMarker
	}
	scheme, rest := raw[:i], raw[i+3:]
	if strings.EqualFold(scheme, "localsecrets") {
		// The rest is key material or a key path; neither belongs in a report.
		return scheme + "://" + redactedURLMarker
	}
	authorityEnd := strings.IndexAny(rest, "/?#")
	authority, tail := rest, ""
	if authorityEnd >= 0 {
		authority, tail = rest[:authorityEnd], rest[authorityEnd:]
	}
	path, query, fragment := splitURLTail(tail)
	return scheme + "://" + maskAuthority(authority) + path + maskQuery(query) + fragment
}

// maskAuthority masks a lone userinfo (a token) and the password of a userinfo
// pair, keeping the user name and the host as written.
func maskAuthority(authority string) string {
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return authority
	}
	userinfo, host := authority[:at], authority[at+1:]
	if colon := strings.Index(userinfo, ":"); colon >= 0 {
		return userinfo[:colon] + ":" + redactedURLMarker + "@" + host
	}
	return redactedURLMarker + "@" + host
}

// splitURLTail splits a URL tail into path, raw query (without ?) and fragment
// (with #).
func splitURLTail(tail string) (path, query, fragment string) {
	if i := strings.Index(tail, "#"); i >= 0 {
		tail, fragment = tail[:i], tail[i:]
	}
	if i := strings.Index(tail, "?"); i >= 0 {
		return tail[:i], tail[i+1:], fragment
	}
	return tail, "", fragment
}

// maskQuery rewrites the credential parameters of a raw query without
// re-encoding the other parameters.
func maskQuery(query string) string {
	if query == "" {
		return ""
	}
	parts := strings.Split(query, "&")
	for i, part := range parts {
		key, _, hasValue := strings.Cut(part, "=")
		if !hasValue {
			continue
		}
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			decoded = key
		}
		if isCredentialQueryKey(decoded) {
			parts[i] = key + "=" + redactedURLMarker
		}
	}
	return "?" + strings.Join(parts, "&")
}

func isCredentialQueryKey(key string) bool {
	for _, k := range credentialQueryKeys {
		if strings.EqualFold(key, k) {
			return true
		}
	}
	return false
}
