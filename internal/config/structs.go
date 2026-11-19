package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
)

// Profiles and port names.
const (
	ProfileEmbedded = "embedded"
	ProfileCloud    = "cloud"

	PortSQL     = "sql"
	PortBlob    = "blob"
	PortBus     = "bus"
	PortSecrets = "secrets"
)

// Source is where an effective setting came from.
type Source string

const (
	// SourceFlag is a command-line flag the operator actually set.
	SourceFlag Source = "flag"
	// SourceEnv is a KIT_ environment variable.
	SourceEnv Source = "env"
	// SourceFile is the YAML configuration file (or stdin).
	SourceFile Source = "file"
	// SourceDefault is the built-in default.
	SourceDefault Source = "default"
)

// Config is the effective configuration of one kit process.
type Config struct {
	Profile  string
	Auth     struct{ Mode string }
	Listen   struct{ Addr string }
	Data     struct{ Dir string }
	Log      struct{ Level, Format string }
	SQL      struct{ URL string }
	Blob     struct{ URL string }
	Bus      struct{ URL string }
	Secrets  struct{ URL string }
	Shutdown struct {
		Timeout time.Duration
	}
}

// setting is one row of the key table. The table order is the order of
// validation errors and of the report.
type setting struct {
	// key is the kebab-case dotted name (`listen.addr`), also the flag name and
	// the base of the environment variable name.
	key string
	// defaultValue returns the built-in default. A nil function means the key
	// has no default of its own; an empty result means the profile has none.
	defaultValue func(profile, dataDir string) string
	// validate checks one effective value and returns a *core.Error naming the
	// key. profile is the effective profile, which port rules need.
	validate func(profile, value string) error
	// apply stores the effective value on Config. Invalid values are stored as
	// far as possible so Validate can report them; callers use the value only
	// after validation.
	apply func(*Config, string)
}

// settings is the single source for the known keys (design §2). Keep the table
// ordered as the design lists it.
var settings = []setting{
	{
		key:          "profile",
		defaultValue: staticDefault(ProfileEmbedded),
		validate:     validateProfile,
		apply:        func(c *Config, v string) { c.Profile = v },
	},
	{
		key:          "auth.mode",
		defaultValue: staticDefault("none"),
		validate:     enumRule("auth.mode", "none", "local", "oidc"),
		apply:        func(c *Config, v string) { c.Auth.Mode = v },
	},
	{
		key:          "listen.addr",
		defaultValue: staticDefault("127.0.0.1:8080"),
		validate:     validateListen,
		apply:        func(c *Config, v string) { c.Listen.Addr = v },
	},
	{
		key:          "data.dir",
		defaultValue: staticDefault("./.kit/data"),
		validate:     validateDataDir,
		apply:        func(c *Config, v string) { c.Data.Dir = v },
	},
	{
		key:          "log.level",
		defaultValue: staticDefault("info"),
		validate:     enumRule("log.level", "debug", "info", "warn", "error"),
		apply:        func(c *Config, v string) { c.Log.Level = v },
	},
	{
		key:          "log.format",
		defaultValue: staticDefault("text"),
		validate:     enumRule("log.format", "text", "json"),
		apply:        func(c *Config, v string) { c.Log.Format = v },
	},
	{
		key:          "sql.url",
		defaultValue: embeddedURLDefault("sqlite", "kit.db"),
		validate:     portRule("sql.url", PortSQL),
		apply:        func(c *Config, v string) { c.SQL.URL = v },
	},
	{
		key:          "blob.url",
		defaultValue: embeddedURLDefault("file", "blobs"),
		validate:     portRule("blob.url", PortBlob),
		apply:        func(c *Config, v string) { c.Blob.URL = v },
	},
	{
		key:          "bus.url",
		defaultValue: embeddedURLDefault("nats-embedded", "nats"),
		validate:     portRule("bus.url", PortBus),
		apply:        func(c *Config, v string) { c.Bus.URL = v },
	},
	{
		key:          "secrets.url",
		defaultValue: embeddedURLDefault("localsecrets", "secrets.key"),
		validate:     portRule("secrets.url", PortSecrets),
		apply:        func(c *Config, v string) { c.Secrets.URL = v },
	},
	{
		key:          "shutdown.timeout",
		defaultValue: staticDefault("10s"),
		validate:     validateShutdownTimeout,
		apply: func(c *Config, v string) {
			if d, err := time.ParseDuration(v); err == nil {
				c.Shutdown.Timeout = d
			}
		},
	},
}

func staticDefault(v string) func(string, string) string {
	return func(string, string) string { return v }
}

// embeddedURLDefault returns a default only for the embedded profile; the
// cloud profile has no default port URL (REQ-CONFIG-010). The URL is built
// with url.URL so a data directory containing '#' or '?' becomes a path, not
// a fragment or query.
func embeddedURLDefault(scheme, leaf string) func(string, string) string {
	return func(profile, dataDir string) string {
		if profile != ProfileEmbedded {
			return ""
		}
		u := url.URL{Scheme: scheme, Path: filepath.ToSlash(filepath.Join(dataDir, leaf))}
		return u.String()
	}
}

func invalidSetting(key, reason string) *core.Error {
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("invalid setting %q: %s", key, reason),
		map[string]any{"key": key})
}

func validateProfile(_, value string) error {
	switch value {
	case ProfileEmbedded, ProfileCloud:
		return nil
	}
	return invalidSetting("profile", `must be one of "embedded" or "cloud"`)
}

func validateListen(_, value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return invalidSetting("listen.addr", "must be host:port with a port between 1 and 65535")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return invalidSetting("listen.addr", "must be host:port with a port between 1 and 65535")
	}
	return nil
}

func validateDataDir(_, value string) error {
	if value == "" {
		return invalidSetting("data.dir", "must not be empty")
	}
	return nil
}

func enumRule(key string, allowed ...string) func(string, string) error {
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = strconv.Quote(a)
	}
	reason := "must be one of " + strings.Join(quoted, ", ")
	return func(_, value string) error {
		for _, a := range allowed {
			if value == a {
				return nil
			}
		}
		return invalidSetting(key, reason)
	}
}

func validateShutdownTimeout(_, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 || d >= 5*time.Minute {
		return invalidSetting("shutdown.timeout", "must be a duration greater than zero and less than 5m")
	}
	return nil
}

// portSchemes is REQ-CONFIG-011 and REQ-CONFIG-012: the schemes each port
// accepts per profile.
var portSchemes = map[string]map[string][]string{
	ProfileEmbedded: {
		PortSQL:     {"sqlite"},
		PortBlob:    {"file"},
		PortBus:     {"nats-embedded"},
		PortSecrets: {"localsecrets"},
	},
	ProfileCloud: {
		PortSQL:     {"postgres"},
		PortBlob:    {"s3", "gs", "azblob"},
		PortBus:     {"nats"},
		PortSecrets: {"awskms", "gcpkms", "hashivault"},
	},
}

func portRule(key, port string) func(string, string) error {
	return func(profile, value string) error {
		if value == "" {
			switch profile {
			case ProfileCloud:
				return invalidSetting(key, `must be set in profile "cloud"`)
			case ProfileEmbedded:
				return invalidSetting(key, "must not be empty")
			}
			return nil
		}
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" {
			// Never include the raw value or the net/url error: both quote it.
			return invalidSetting(key, "URL cannot be parsed")
		}
		switch profile {
		case ProfileEmbedded, ProfileCloud:
			if !schemeAllowed(profile, port, u.Scheme) {
				return invalidSetting(key,
					fmt.Sprintf("scheme %q is not accepted by profile %q", u.Scheme, profile))
			}
		}
		return nil
	}
}

func schemeAllowed(profile, port, scheme string) bool {
	for _, s := range portSchemes[profile][port] {
		if s == scheme {
			return true
		}
	}
	return false
}

// absoluteDataDir resolves data.dir for URL defaults. A failure keeps the
// configured value, which is still a usable relative path.
func absoluteDataDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}
