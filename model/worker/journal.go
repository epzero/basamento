package worker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/epzero/basamento/core"
)

// JournalEntry is one line of a worker's journal: a lease it holds, the
// provider handle it was given, or a result it has not delivered yet
// (ADR-0012 §7). It is a file, not a database.
type JournalEntry struct {
	Kind               string          `json:"kind"` // lease | handle | result | done
	ClientRef          string          `json:"client_ref"`
	Model              string          `json:"model,omitempty"`
	Attempt            int             `json:"attempt,omitempty"`
	LeaseExpiresUnixMS int64           `json:"lease_expires_unix_ms,omitempty"`
	ProviderHandle     string          `json:"provider_handle,omitempty"`
	Result             json.RawMessage `json:"result,omitempty"`
	ErrorCode          string          `json:"error_code,omitempty"`
	CostMpts           int64           `json:"cost_mpts,omitempty"`
}

// Journal is a worker's crash-safe record of its leases and undelivered
// results. A worker that crashed, restarted or lost its network replays it and
// resumes its invocations; it never starts a second paid call on its own.
type Journal struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// OpenJournal opens or creates the journal in a worker data directory.
func OpenJournal(dir string) (*Journal, error) {
	if dir == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "worker: journal needs a data directory", nil)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, core.NewError(core.CodeInternal, "worker: cannot create data directory", map[string]any{"cause": err.Error()})
	}
	path := filepath.Join(dir, "worker.journal")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "worker: cannot open journal", map[string]any{"cause": err.Error()})
	}
	return &Journal{path: path, file: f}, nil
}

// Path is the journal file path.
func (j *Journal) Path() string { return j.path }

// Record appends one entry and flushes it before returning.
func (j *Journal) Record(e JournalEntry) error {
	if e.ClientRef == "" && e.Kind != "done" {
		return core.NewError(core.CodeInvalidArgument, "worker: journal entry has no client ref", nil)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return core.NewError(core.CodeInvalidArgument, "worker: journal entry is not encodable", nil)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return core.NewError(core.CodeInternal, "worker: journal is closed", nil)
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return core.NewError(core.CodeInternal, "worker: cannot write journal", map[string]any{"cause": err.Error()})
	}
	return j.file.Sync()
}

// Entries reads the journal back in order.
func (j *Journal) Entries() ([]JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.Open(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, core.NewError(core.CodeInternal, "worker: cannot read journal", map[string]any{"cause": err.Error()})
	}
	defer f.Close()
	var out []JournalEntry
	scn := bufio.NewScanner(f)
	scn.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scn.Scan() {
		if len(scn.Bytes()) == 0 {
			continue
		}
		var e JournalEntry
		if err := json.Unmarshal(scn.Bytes(), &e); err != nil {
			return nil, core.NewError(core.CodeInternal, "worker: journal is corrupt", map[string]any{"cause": err.Error()})
		}
		out = append(out, e)
	}
	if err := scn.Err(); err != nil {
		return nil, core.NewError(core.CodeInternal, "worker: cannot read journal", map[string]any{"cause": err.Error()})
	}
	return out, nil
}

// Pending returns the leases that have no result yet, keyed by client ref in
// the order they were first leased.
func (j *Journal) Pending() ([]JournalEntry, error) {
	entries, err := j.Entries()
	if err != nil {
		return nil, err
	}
	order := []string{}
	byRef := map[string]JournalEntry{}
	finished := map[string]bool{}
	for _, e := range entries {
		switch e.Kind {
		case "lease":
			if _, ok := byRef[e.ClientRef]; !ok {
				order = append(order, e.ClientRef)
			}
			byRef[e.ClientRef] = e
		case "handle":
			if prev, ok := byRef[e.ClientRef]; ok {
				prev.ProviderHandle = e.ProviderHandle
				byRef[e.ClientRef] = prev
			}
		case "done":
			finished[e.ClientRef] = true
		}
	}
	out := make([]JournalEntry, 0, len(order))
	for _, ref := range order {
		if finished[ref] {
			continue
		}
		out = append(out, byRef[ref])
	}
	return out, nil
}

// Close closes the journal.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	if err != nil {
		return fmt.Errorf("worker: close journal: %w", err)
	}
	return nil
}
