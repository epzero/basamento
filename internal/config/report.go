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
// A password in a URL setting prints as *** (REQ-CONFIG-008). A URL setting
// that cannot be parsed prints as *** in full, since no part of it can be
// classified as safe.
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

func maskedURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "***"
	}
	if u.User == nil {
		return u.String()
	}
	if _, has := u.User.Password(); !has {
		return u.String()
	}
	user := url.User(u.User.Username()).String()
	u.User = nil
	rendered := u.String()
	prefix := u.Scheme + "://"
	if !strings.HasPrefix(rendered, prefix) {
		// Not a hierarchical URL: keep only what cannot be a secret.
		return prefix + user + ":***"
	}
	return prefix + user + ":***@" + strings.TrimPrefix(rendered, prefix)
}
