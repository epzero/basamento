package sqltest

import (
	"os"
	"strings"
)

// BDDPostgresCacheDir names the shared embedded-postgres bundle cache (ADR-0015).
const BDDPostgresCacheDir = "BDD_POSTGRES_CACHE_DIR"

// GateStrict reports whether GATE_STRICT is set (hermetic BDD, no bundle download).
func GateStrict() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GATE_STRICT")))
	return v == "1" || v == "true" || v == "yes"
}

// PostgresCacheDir returns the bundle cache directory, or empty for the library default.
func PostgresCacheDir() string {
	return os.Getenv(BDDPostgresCacheDir)
}
