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
	"auth.mode",
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

// wiringFlags are operational flags of `kit serve` that are not configuration
// settings: they wire the worker side (a listener for workers that dial in and
// the pool and remotes the platform owns).
var wiringFlags = []string{
	"worker.listen",
	"worker.pool",
	"worker.pool-tenant",
	"worker.pool-token-file",
	"worker.remote",
	"worker.remote-model",
	"worker.remote-labels",
	"worker.remote-manifest",
	"worker.tls.cert-file",
	"worker.tls.key-file",
	"worker.tls.ca-file",
}

type flags struct {
	fs         *flag.FlagSet
	settings   map[string]*string
	wiring     map[string]string
	configPath *string
}

func newFlags(name string) *flags {
	f := &flags{
		fs:       flag.NewFlagSet(name, flag.ContinueOnError),
		settings: map[string]*string{},
		wiring:   map[string]string{},
	}
	f.fs.SetOutput(os.Stderr)
	f.configPath = f.fs.String("config", "", "configuration file (YAML); \"-\" reads stdin")
	for _, key := range settingFlags {
		f.settings[key] = f.fs.String(key, "", "setting "+key)
	}
	for _, key := range wiringFlags {
		f.wiring[key] = ""
		f.fs.StringVar(new(string), key, "", "worker wiring "+key)
	}
	return f
}

func (f *flags) parse(args []string) error {
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	f.fs.Visit(func(fl *flag.Flag) {
		if _, ok := f.wiring[fl.Name]; ok {
			f.wiring[fl.Name] = fl.Value.String()
		}
	})
	return nil
}

// workerWiring returns the worker-side flags the operator set.
func (f *flags) workerWiring() map[string]string {
	out := make(map[string]string)
	for key, value := range f.wiring {
		if value != "" {
			out[key] = value
		}
	}
	return out
}

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
