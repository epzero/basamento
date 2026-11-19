package secrets

import (
	"fmt"
	"net/url"
	"strings"
)

// publicURLID returns the key service URL without secrets (design §2).
func publicURLID(raw string) (string, error) {
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return "", errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
	if scheme == "localsecrets" {
		return "", errInvalidArgument("localsecrets uses a key fingerprint, not a URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "", errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
	if u.Host == "" && u.Path == "" {
		return "", errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
	u.User = nil
	u.Fragment = ""
	if u.RawQuery != "" {
		q := u.Query()
		u.RawQuery = q.Encode()
	}
	out := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawQuery: u.RawQuery}
	return out.String(), nil
}
