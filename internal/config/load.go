package config

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

// Result is an effective configuration and the source of each key.
type Result struct {
	Config  Config
	Sources map[string]Source // key -> flag, env, file, default

	values map[string]string
}

// Setting returns the effective string value of key, where it came from, and
// whether the key has a value at all. A cloud port URL has no default, so it
// reports false until a source sets it (REQ-CONFIG-010).
func (r Result) Setting(key string) (string, Source, bool) {
	v, ok := r.values[key]
	if !ok {
		return "", "", false
	}
	return v, r.Sources[key], true
}

// Options are the sources Load reads. Every field is optional.
type Options struct {
	File  string            // configuration file path; empty = none, "-" = stdin
	Env   []string          // os.Environ() form
	Stdin io.Reader         // for YAML from stdin ("-"), optional
	Flags map[string]string // flags actually set on the command line
}

// Load reads the sources in precedence order and returns the effective
// configuration. It returns an error only when a configuration file cannot be
// read or parsed; every other problem is left to Result.Validate.
func Load(opts Options) (Result, error) {
	fileK, err := loadFile(opts)
	if err != nil {
		return Result{}, err
	}
	envValues := envSettings(opts.Env)
	flagValues := knownSettings(opts.Flags)

	// The profile and the data directory decide the remaining defaults, so
	// pick them before filling those in.
	profile, ok := pick(fileK, envValues, flagValues, "profile")
	if !ok {
		profile = ProfileEmbedded
	}
	dataDir, ok := pick(fileK, envValues, flagValues, "data.dir")
	if !ok {
		dataDir = "./.kit/data"
	}
	absDataDir := absoluteDataDir(dataDir)

	defaults := map[string]any{}
	for _, s := range settings {
		if s.defaultValue == nil {
			continue
		}
		if d := s.defaultValue(profile, absDataDir); d != "" {
			defaults[s.key] = d
		}
	}

	k := koanf.New(".")
	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return Result{}, fmt.Errorf("config defaults: %w", err)
	}
	if fileK != nil {
		// Load the file's raw map through confmap so a literal dotted YAML key
		// ("sql.url") is unflattened exactly like the flat environment and flag
		// maps; merging a flat map into the nested one would keep two keys.
		if err := k.Load(confmap.Provider(fileK.Raw(), "."), nil); err != nil {
			return Result{}, fmt.Errorf("config file: %w", err)
		}
	}
	if err := k.Load(confmap.Provider(stringMap(envValues), "."), nil); err != nil {
		return Result{}, fmt.Errorf("config environment: %w", err)
	}
	if err := k.Load(confmap.Provider(stringMap(flagValues), "."), nil); err != nil {
		return Result{}, fmt.Errorf("config flags: %w", err)
	}

	res := Result{Sources: map[string]Source{}, values: map[string]string{}}
	for _, s := range settings {
		v := k.String(s.key)
		if isPortKey(s.key) {
			v = strings.ReplaceAll(v, "$(data.dir)", absDataDir)
		}
		s.apply(&res.Config, v)
		switch {
		case hasKey(flagValues, s.key):
			res.values[s.key] = v
			res.Sources[s.key] = SourceFlag
		case hasKey(envValues, s.key):
			res.values[s.key] = v
			res.Sources[s.key] = SourceEnv
		case fileK != nil && fileK.Exists(s.key):
			res.values[s.key] = v
			res.Sources[s.key] = SourceFile
		case defaults[s.key] != nil:
			res.values[s.key] = v
			res.Sources[s.key] = SourceDefault
		}
	}
	return res, nil
}

// loadFile reads the YAML configuration file, if one applies. A file the
// operator did not name is optional: it is used when present.
func loadFile(opts Options) (*koanf.Koanf, error) {
	path := opts.File
	if path == "" {
		return nil, nil
	}
	explicit := opts.Flags["config"] != "" || path == "-"
	k := koanf.New(".")
	if path == "-" {
		if opts.Stdin == nil {
			return nil, invalidFile("configuration file could not be read", path)
		}
		b, err := io.ReadAll(opts.Stdin)
		if err != nil {
			return nil, invalidFile("configuration file could not be read", path)
		}
		if err := k.Load(rawbytes.Provider(b), yaml.Parser()); err != nil {
			return nil, invalidFile("configuration file could not be parsed", path)
		}
		return k, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) && !explicit {
			return nil, nil
		}
		return nil, invalidFile("configuration file could not be read", path)
	}
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, invalidFile("configuration file could not be parsed", path)
	}
	return k, nil
}

func invalidFile(reason, path string) error {
	return invalidSetting("config", fmt.Sprintf("%s: %s", reason, path))
}

// envSettings maps KIT_<KEY> variables to setting keys (REQ-CONFIG-002).
// Variables that name no known key are ignored.
func envSettings(environ []string) map[string]string {
	byName := make(map[string]string, len(settings))
	for _, s := range settings {
		byName[EnvironName(s.key)] = s.key
	}
	out := map[string]string{}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key, ok := byName[name]; ok {
			out[key] = value
		}
	}
	return out
}

// EnvironName is the environment variable of a setting key: KIT_<KEY>, with
// every dot written as an underscore and every letter uppercased
// (REQ-CONFIG-002).
func EnvironName(key string) string {
	return "KIT_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

func knownSettings(in map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range settings {
		if v, ok := in[s.key]; ok {
			out[s.key] = v
		}
	}
	return out
}

// pick resolves one key from the highest source before the defaults are known.
func pick(fileK *koanf.Koanf, envValues, flagValues map[string]string, key string) (string, bool) {
	if v, ok := flagValues[key]; ok {
		return v, true
	}
	if v, ok := envValues[key]; ok {
		return v, true
	}
	if fileK != nil && fileK.Exists(key) {
		return fileK.String(key), true
	}
	return "", false
}

func stringMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func hasKey(in map[string]string, key string) bool {
	_, ok := in[key]
	return ok
}

func isPortKey(key string) bool {
	switch key {
	case "sql.url", "blob.url", "bus.url", "secrets.url":
		return true
	}
	return false
}
