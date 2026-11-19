// Package config loads, validates and reports the configuration of the kit
// binary (specs/config, WP-L1-05).
//
// One table (see structs.go) is the single source for every known key: its
// kebab-case name, its default, the Go field it fills and the rule that
// validates it. Loading merges command-line flags, KIT_ environment variables,
// a YAML configuration file and the defaults in that order; the source of each
// effective key is kept so `kit config validate` can show where it came from.
//
// The package holds no port implementation and imports core only (ADR-0016).
// Values that select a port are returned as validated URLs; the composition
// root, cmd/kit, opens the implementations.
//
// The server lifecycle in serve.go is shared with cmd/kit so the component
// scenarios can drive it in-process; it owns no listener or port itself.
package config
