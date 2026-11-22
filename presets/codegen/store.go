package codegen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/epzero/basamento/core"
)

// Record is the durable version lifecycle record: the current version and
// every version's status. It lives next to the scores file, so a rejection
// survives a re-run (REQ-PRESET-016..018).
type Record struct {
	Current  string                     `json:"current,omitempty"`
	Versions map[string]RecordedVersion `json:"versions"`
}

// RecordedVersion is one version's persisted lifecycle state.
type RecordedVersion struct {
	ID       string         `json:"id"`
	Status   VersionStatus  `json:"status"`
	Code     core.Code      `json:"code,omitempty"`
	Case     string         `json:"case,omitempty"`
	Scores   map[string]int `json:"scores,omitempty"`
	GateOpen bool           `json:"gate_open,omitempty"`
}

// RecordPath returns the record path next to a scores file.
func RecordPath(scoresPath string) string {
	ext := filepath.Ext(scoresPath)
	return strings.TrimSuffix(scoresPath, ext) + ".versions.json"
}

// LoadRegistry loads the durable record at path, or returns an empty registry
// over suite when no record exists. A malformed record is an error: the
// lifecycle fails closed rather than guessing.
func LoadRegistry(path string, suite []string) (*Registry, error) {
	reg := NewRegistry(suite)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, invalid("codegen: version record %s: %s", path, err.Error())
	}
	if err := rec.validate(); err != nil {
		return nil, invalid("codegen: version record %s: %s", path, err.Error())
	}
	reg.Current = rec.Current
	for id, rv := range rec.Versions {
		reg.Versions[id] = &Version{
			ID:       id,
			Sources:  DefaultSources(),
			Scores:   rv.Scores,
			Status:   rv.Status,
			Code:     rv.Code,
			Case:     rv.Case,
			GateOpen: rv.GateOpen,
		}
	}
	return reg, nil
}

func (rec Record) validate() error {
	if rec.Current != "" {
		if _, ok := rec.Versions[rec.Current]; !ok {
			return fmt.Errorf("current version %q is not recorded", rec.Current)
		}
	}
	for id, rv := range rec.Versions {
		if id == "" {
			return fmt.Errorf("a version has an empty id")
		}
		if rv.ID != "" && rv.ID != id {
			return fmt.Errorf("version %q records id %q", id, rv.ID)
		}
		if !validStatus(rv.Status) {
			return fmt.Errorf("version %q has unknown status %q", id, rv.Status)
		}
		if rv.GateOpen && rv.Status != StatusWaiting {
			return fmt.Errorf("version %q has an open gate in status %q", id, rv.Status)
		}
		if rv.Status == StatusWaiting && !rv.GateOpen {
			return fmt.Errorf("version %q is waiting with a closed gate", id)
		}
	}
	return nil
}

func validStatus(status VersionStatus) bool {
	switch status {
	case StatusProposed, StatusWaiting, StatusAccepted, StatusSuperseded, StatusRejected:
		return true
	}
	return false
}

// SaveRegistry writes the registry's record at path atomically.
func SaveRegistry(path string, reg *Registry) error {
	rec := Record{Current: reg.Current, Versions: map[string]RecordedVersion{}}
	for id, v := range reg.Versions {
		rec.Versions[id] = RecordedVersion{
			ID: v.ID, Status: v.Status, Code: v.Code, Case: v.Case,
			Scores: v.Scores, GateOpen: v.GateOpen,
		}
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".versions-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
