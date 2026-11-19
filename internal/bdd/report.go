package bdd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/epzero/basamento/internal/bdd/sqltest"
)

// bddReport is the Postgres pass summary written for the gate (no secrets).
type bddReport struct {
	Outcome        string         `json:"outcome"`
	PostgresServer string         `json:"postgres_server,omitempty"`
	SkipRuns       map[string]int `json:"skip_runs"`
}

// WriteBDDReport persists the scenario run outcome before TestFeatures returns.
func WriteBDDReport(failed bool) error {
	src := sqltest.PostgresServerSource()
	outcome := "pass"
	if failed {
		outcome = "fail"
	}
	skips := sqltest.SkipRuns()
	if skips == nil {
		skips = map[string]int{}
	}
	rep := bddReport{
		Outcome:        outcome,
		PostgresServer: src,
		SkipRuns:       skips,
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".gate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "bdd.json"))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(rep)
	err = errors.Join(err, f.Close())
	return err
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("bdd: go.mod not found")
		}
		dir = parent
	}
}
