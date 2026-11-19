package config

import "net/url"

// Implementation returns the implementation a port URL selects, by scheme:
// `gs` selects `gcs` and every other scheme keeps its name (REQ-CONFIG-013,
// REQ-CONFIG-012). The profile narrowed the accepted schemes when Validate
// passed; Implementation is never called on an unvalidated URL.
func (c Config) Implementation(port string) string {
	var raw string
	switch port {
	case PortSQL:
		raw = c.SQL.URL
	case PortBlob:
		raw = c.Blob.URL
	case PortBus:
		raw = c.Bus.URL
	case PortSecrets:
		raw = c.Secrets.URL
	default:
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme == "gs" {
		return "gcs"
	}
	return u.Scheme
}
