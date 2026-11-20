package main

import (
	"flag"
	"io"
	"os"

	"github.com/epzero/basamento/internal/config"
)

// settingFlags are the settings an operator may set on the command line. A
// flag at its zero value does not override a lower source: only flags the
// operator typed are read (design §3).
var settingFlags = []string{
	"profile",
	"listen.addr",
	"data.dir",
	"log.level",
	"log.format",
	"sql.url",
	"blob.url",
	"bus.url",
	"secrets.url",
	"shutdown.timeout",
}

type flags struct {
	fs         *flag.FlagSet
	settings   map[string]*string
	configPath *string
}

func newFlags(name string) *flags {
	f := &flags{fs: flag.NewFlagSet(name, flag.ContinueOnError), settings: map[string]*string{}}
	f.fs.SetOutput(os.Stderr)
	f.configPath = f.fs.String("config", "", "configuration file (YAML); \"-\" reads stdin")
	for _, key := range settingFlags {
		f.settings[key] = f.fs.String(key, "", "setting "+key)
	}
	return f
}

func (f *flags) parse(args []string) error { return f.fs.Parse(args) }

// options builds the loader input from the flags actually set.
func (f *flags) options(stdin io.Reader) config.Options {
	set := make(map[string]string)
	f.fs.Visit(func(fl *flag.Flag) {
		if value, ok := f.settings[fl.Name]; ok {
			set[fl.Name] = *value
		}
	})
	path := *f.configPath
	auto := ""
	if path == "" {
		auto = "kit.yaml"
	}
	return config.Options{
		File:     path,
		AutoFile: auto,
		Env:      os.Environ(),
		Stdin:    stdin,
		Flags:    set,
	}
}
