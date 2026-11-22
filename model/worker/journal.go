package worker

import (
	"bytes"
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
	Request            json.RawMessage `json:"request,omitempty"`
	State              string          `json:"state,omitempty"`
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

// Entries reads the journal back in order. A crash can leave the last line
// torn; that line is dropped and the file is repaired so the next record
// appends after the last complete entry. A malformed line anywhere else is
// still corruption.
func (j *Journal) Entries() ([]JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	data, err := os.ReadFile(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, core.NewError(core.CodeInternal, "worker: cannot read journal", map[string]any{"cause": err.Error()})
	}
	var out []JournalEntry
	offset := 0
	for offset < len(data) {
		rest := data[offset:]
		nl := bytes.IndexByte(rest, '\n')
		complete := nl >= 0
		line := rest
		if complete {
			line = rest[:nl]
		}
		if len(line) == 0 {
			if !complete {
				break
			}
			offset += nl + 1
			continue
		}
		var e JournalEntry
		if err := json.Unmarshal(line, &e); err != nil {
			if !complete {
				// The crash left a torn trailing line. Drop it and cut the
				// file back to the last complete entry so the next Record
				// cannot append behind a partial line.
				j.truncate(offset)
				break
			}
			return nil, core.NewError(core.CodeInternal, "worker: journal is corrupt", map[string]any{"cause": err.Error()})
		}
		out = append(out, e)
		if !complete {
			// A complete entry that lost only its newline is kept, and the
			// newline is restored so the next record starts on a new line.
			if j.file != nil {
				if _, err := j.file.Write([]byte{'\n'}); err != nil {
					return nil, core.NewError(core.CodeInternal, "worker: cannot repair journal", map[string]any{"cause": err.Error()})
				}
				_ = j.file.Sync()
			}
			break
		}
		offset += nl + 1
	}
	return out, nil
}

// truncate cuts the journal back to size, ignoring a closed or already removed
// file: the caller has the entries it can trust either way.
func (j *Journal) truncate(size int) {
	if j.file == nil {
		return
	}
	if err := j.file.Truncate(int64(size)); err != nil {
		return
	}
	_ = j.file.Sync()
}

// Pending returns the leases and results that have no done entry yet, keyed by
// client ref in the order they were first leased. A result entry carries the
// terminal state a restart has to redeliver (ADR-0012 §7).
func (j *Journal) Pending() ([]JournalEntry, error) {
	entries, err := j.Entries()
	if err != nil {
		return nil, err
	}
	order := []string{}
	leases := map[string]JournalEntry{}
	results := map[string]JournalEntry{}
	finished := map[string]bool{}
	for _, e := range entries {
		switch e.Kind {
		case "lease":
			if _, ok := leases[e.ClientRef]; !ok {
				order = append(order, e.ClientRef)
			}
			leases[e.ClientRef] = e
			// A later lease is a new attempt: an earlier done or result
			// belongs to an attempt that no longer holds the work.
			delete(finished, e.ClientRef)
			delete(results, e.ClientRef)
		case "handle":
			if prev, ok := leases[e.ClientRef]; ok {
				prev.ProviderHandle = e.ProviderHandle
				leases[e.ClientRef] = prev
			}
		case "result":
			if _, ok := leases[e.ClientRef]; !ok && results[e.ClientRef].ClientRef == "" {
				order = append(order, e.ClientRef)
			}
			results[e.ClientRef] = e
		case "done":
			finished[e.ClientRef] = true
		}
	}
	out := make([]JournalEntry, 0, len(order))
	for _, ref := range order {
		if finished[ref] {
			continue
		}
		if r, ok := results[ref]; ok {
			if l, ok := leases[ref]; ok {
				r.Model = l.Model
				r.Attempt = l.Attempt
				r.LeaseExpiresUnixMS = l.LeaseExpiresUnixMS
				r.ProviderHandle = l.ProviderHandle
				r.Request = l.Request
			}
			out = append(out, r)
			continue
		}
		if l, ok := leases[ref]; ok {
			out = append(out, l)
		}
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
