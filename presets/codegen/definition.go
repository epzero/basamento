package codegen

import (
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	flowir "github.com/epzero/basamento/flow/ir"
	codegenir "github.com/epzero/basamento/presets/codegen/ir"
)

//go:embed prompts/*.md evals/*.json
var assets embed.FS

// Sources is one preset version's definition: its flows, prompt files and eval
// recordings, keyed by path. The version is the content hash of all three
// (REQ-PRESET-014, specs/presets/design.md §1).
type Sources struct {
	Flows   map[string]*flow.IR
	Prompts map[string][]byte
	Evals   map[string][]byte
}

// DefaultSources loads the preset's embedded flows, prompts and evals.
func DefaultSources() Sources {
	flows := codegenir.All()
	src := Sources{
		Flows:   map[string]*flow.IR{},
		Prompts: map[string][]byte{},
		Evals:   map[string][]byte{},
	}
	for name, def := range flows {
		src.Flows[name] = def
	}
	for _, dir := range []string{"prompts", "evals"} {
		entries, err := fs.ReadDir(assets, dir)
		if err != nil {
			panic("codegen: embedded " + dir + ": " + err.Error())
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := dir + "/" + entry.Name()
			data, err := assets.ReadFile(path)
			if err != nil {
				panic("codegen: embedded " + path + ": " + err.Error())
			}
			if dir == "prompts" {
				src.Prompts[path] = data
			} else {
				src.Evals[path] = data
			}
		}
	}
	return src
}

// Compile checks what the preset can know before a version runs: every flow is
// a valid flow and every prompt reference resolves. A version that does not
// compile is rejected with invalid_argument before any eval runs
// (REQ-PRESET-016).
func (s Sources) Compile() error {
	if len(s.Flows) == 0 {
		return invalid("codegen: the preset has no flows")
	}
	for name, def := range s.Flows {
		if def == nil {
			return invalid("codegen: flow %q is nil", name)
		}
		if err := flowir.Validate(def); err != nil {
			return withCode(err, core.CodeInvalidArgument, "codegen: flow %q does not compile", name)
		}
		if _, err := flowir.Canonical(def); err != nil {
			return withCode(err, core.CodeInvalidArgument, "codegen: flow %q does not compile", name)
		}
	}
	for _, name := range []string{FlowBootstrap, FlowIterate} {
		def := s.Flows[name]
		if def == nil {
			return invalid("codegen: flow %q is required", name)
		}
		for _, stage := range codegenir.ExpectedStages() {
			if _, ok := def.Nodes[stage]; !ok {
				return invalid("codegen: flow %q has no stage %q", name, stage)
			}
		}
	}
	for _, ref := range promptRefs(s.Flows) {
		if _, ok := s.Prompts[ref]; !ok {
			return invalid("codegen: prompt %q does not compile: no such prompt file", ref)
		}
	}
	return nil
}

// Version returns the version id: the lowercase hex SHA-256 of the canonical
// flows plus the prompt and eval files, sorted by path. It is the definition a
// run pins.
func (s Sources) Version() (string, error) {
	h := sha256.New()
	names := sortedKeys(s.Flows)
	for _, name := range names {
		raw, err := flowir.Canonical(s.Flows[name])
		if err != nil {
			return "", err
		}
		writeChunk(h, []byte(name), raw)
	}
	writeFiles(h, "prompt", s.Prompts)
	writeFiles(h, "eval", s.Evals)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LoadEval returns the recorded judge scores of one eval case, in the order the
// fixture lists them.
func (s Sources) LoadEval(caseID string) ([]map[string]int, error) {
	raw, ok := s.Evals["evals/"+caseID+".json"]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, fmt.Sprintf("codegen: eval case %q has no recording", caseID), nil)
	}
	var out []map[string]int
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, invalid("codegen: eval case %q: %s", caseID, err.Error())
	}
	return out, nil
}

// Mean returns the arithmetic mean of metric over a case's recorded runs. The
// judge itself is a nightly live call; the gate replays these recordings
// (ADR-0010, specs/presets/design.md §4).
func (s Sources) Mean(caseID, metric string) (float64, error) {
	runs, err := s.LoadEval(caseID)
	if err != nil {
		return 0, err
	}
	if len(runs) == 0 {
		return 0, core.NewError(core.CodeNotFound, fmt.Sprintf("codegen: eval case %q has no runs", caseID), nil)
	}
	total := 0
	for _, run := range runs {
		score, ok := run[metric]
		if !ok {
			return 0, core.NewError(core.CodeNotFound, fmt.Sprintf("codegen: eval case %q has no %q score", caseID, metric), nil)
		}
		total += score
	}
	return float64(total) / float64(len(runs)), nil
}

// promptRefs walks the flows for every "prompt" string value.
func promptRefs(flows map[string]*flow.IR) []string {
	seen := map[string]bool{}
	var walk func(value any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if key == "prompt" {
					if text, ok := item.(string); ok && text != "" {
						seen[text] = true
					}
				}
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	for _, def := range flows {
		if def == nil {
			continue
		}
		for _, node := range def.Nodes {
			if len(node.Spec) == 0 {
				continue
			}
			var spec any
			if err := json.Unmarshal(node.Spec, &spec); err == nil {
				walk(spec)
			}
		}
	}
	refs := make([]string, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func writeFiles(h interface{ Write([]byte) (int, error) }, kind string, files map[string][]byte) {
	for _, path := range sortedKeys(files) {
		writeChunk(h, []byte(kind+":"+path), files[path])
	}
}

func writeChunk(h interface{ Write([]byte) (int, error) }, label, data []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(label)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(label)
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(data)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...), nil)
}

func withCode(err error, code core.Code, format string, args ...any) *core.Error {
	return &core.Error{Code: code, Message: fmt.Sprintf(format, args...) + ": " + err.Error(), Cause: err}
}
