package prompt

import (
	"sort"
	"strings"
	"sync"

	"github.com/epzero/basamento/core"
)

// Registry stores versioned templates. A Registry is safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	byRef  map[string]*Template
	byHash map[string]string
}

// NewRegistry returns an empty template registry.
func NewRegistry() *Registry {
	return &Registry{
		byRef:  map[string]*Template{},
		byHash: map[string]string{},
	}
}

// RegisterResult is returned when a template is registered.
type RegisterResult struct {
	Warnings []Finding
}

// Register validates, lints and stores a template under id@version.
func (r *Registry) Register(t *Template) (*RegisterResult, error) {
	if t == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "template required", nil)
	}
	ref := t.ID + "@" + t.Version
	if v, ok := CheckDeclaredVars(t); !ok {
		return nil, core.NewError(core.CodeInvalidArgument, "undeclared variable", map[string]any{"variable": v})
	}
	findings := LintTemplate(t)
	var errs, warns []Finding
	for _, f := range findings {
		if f.Severity == "error" {
			errs = append(errs, f)
		} else {
			warns = append(warns, f)
		}
	}
	if len(errs) > 0 {
		return nil, lintRegisterError(errs)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byRef[ref]; ok {
		if TemplateContentHash(existing) == TemplateContentHash(t) {
			return &RegisterResult{Warnings: warns}, nil
		}
		return nil, core.NewError(core.CodeConflict, "template version conflict", nil)
	}
	h := TemplateContentHash(t)
	cp := cloneTemplate(t)
	r.byRef[ref] = cp
	r.byHash[h] = ref
	return &RegisterResult{Warnings: warns}, nil
}

func lintRegisterError(errs []Finding) error {
	details := map[string]any{"lint": lintDetails(errs)}
	return core.NewError(core.CodeInvalidArgument, "template lint failed", details)
}

func lintDetails(errs []Finding) []map[string]string {
	out := make([]map[string]string, len(errs))
	for i, e := range errs {
		out[i] = map[string]string{"rule": e.Rule, "segment": e.Segment, "severity": e.Severity}
	}
	return out
}

// Resolve returns the template for a pinned reference.
func (r *Registry) Resolve(ref string) (*Template, error) {
	id, ver, err := ParseReference(ref)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "invalid template reference", nil)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := id + "@" + ver
	t, ok := r.byRef[key]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "template not found", nil)
	}
	return cloneTemplate(t), nil
}

// VersionCount returns how many versions are registered for id.
func (r *Registry) VersionCount(id string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	prefix := id + "@"
	for k := range r.byRef {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// Assemble builds a prompt from a registered template.
func (r *Registry) Assemble(ref string, inputs map[string]string, history []HistoryEntry) (*Assembled, error) {
	t, err := r.Resolve(ref)
	if err != nil {
		return nil, err
	}
	return Assemble(t, ref, inputs, history)
}

func cloneTemplate(t *Template) *Template {
	cp := *t
	cp.Inputs = append([]Input(nil), t.Inputs...)
	cp.Segments = append([]Segment(nil), t.Segments...)
	return &cp
}

func inputIndex(t *Template) map[string]Input {
	m := map[string]Input{}
	for _, in := range t.Inputs {
		trust := in.Trust
		if trust == "" {
			trust = TrustUntrusted
		}
		m[in.Name] = Input{Name: in.Name, Trust: trust}
	}
	return m
}

// Assemble builds a prompt from a template (caller must have resolved it).
func Assemble(t *Template, ref string, inputs map[string]string, history []HistoryEntry) (*Assembled, error) {
	idx := inputIndex(t)
	if err := validateInputs(idx, inputs); err != nil {
		return nil, err
	}
	if err := validateHistory(history); err != nil {
		return nil, err
	}
	ordered := orderSegments(t.Segments)
	dropped := 0
	hist := append([]HistoryEntry(nil), history...)
	for {
		parts, sizes, err := renderAll(t, ordered, idx, inputs, hist)
		if err != nil {
			return nil, err
		}
		if name, sz, bud := firstSegmentOverBudget(t, sizes); name != "" {
			return nil, segmentBudgetError(name, sz, bud)
		}
		histOver := historyOverBudget(t, sizes)
		totalOver := t.TotalBudget > 0 && totalSize(sizes) > t.TotalBudget
		if !histOver && !totalOver {
			return finalizeAssembled(ref, parts, dropped), nil
		}
		if len(hist) == 0 {
			if totalOver {
				return nil, totalBudgetError(t.TotalBudget, totalSize(sizes))
			}
			if histOver {
				return nil, segmentBudgetError(historySegName(t), sizes[historySegName(t)], historyBudget(t))
			}
		}
		hist = hist[1:]
		dropped++
	}
}

func validateInputs(decl map[string]Input, inputs map[string]string) error {
	for name := range decl {
		if _, ok := inputs[name]; !ok {
			return core.NewError(core.CodeInvalidArgument, "missing input", map[string]any{"input": name})
		}
	}
	for name := range inputs {
		if _, ok := decl[name]; !ok {
			return core.NewError(core.CodeInvalidArgument, "undeclared input", map[string]any{"input": name})
		}
	}
	return nil
}

var kindOrder = map[Kind]int{
	KindStatic:     0,
	KindSemiStatic: 1,
	KindDynamic:    2,
	KindHistory:    3,
	KindUser:       4,
}

func orderSegments(segs []Segment) []Segment {
	type keyed struct {
		seg Segment
		idx int
		ord int
	}
	keyedSegs := make([]keyed, len(segs))
	for i, s := range segs {
		keyedSegs[i] = keyed{seg: s, idx: i, ord: kindOrder[s.Kind]}
	}
	sort.SliceStable(keyedSegs, func(i, j int) bool {
		if keyedSegs[i].ord != keyedSegs[j].ord {
			return keyedSegs[i].ord < keyedSegs[j].ord
		}
		return keyedSegs[i].idx < keyedSegs[j].idx
	})
	out := make([]Segment, len(segs))
	for i, k := range keyedSegs {
		out[i] = k.seg
	}
	return out
}

func renderAll(t *Template, ordered []Segment, idx map[string]Input, inputs map[string]string, history []HistoryEntry) ([]AssembledPart, map[string]int, error) {
	parts := make([]AssembledPart, 0, len(ordered))
	sizes := map[string]int{}
	trustedVals := map[string]string{}
	rawSizes := map[string]int{}
	for name, in := range idx {
		v := inputs[name]
		rawSizes[name] = CodePointCount(v)
		if in.Trust == TrustTrusted {
			trustedVals[name] = v
		}
	}
	for _, seg := range ordered {
		switch seg.Kind {
		case KindHistory:
			p, sz, err := renderHistory(seg, history)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, p)
			sizes[seg.Name] = sz
		default:
			p, sz, err := renderSegment(seg, idx, inputs, trustedVals, rawSizes)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, p)
			sizes[seg.Name] = sz
		}
	}
	return parts, sizes, nil
}

func renderSegment(seg Segment, idx map[string]Input, inputs, trusted map[string]string, rawSizes map[string]int) (AssembledPart, int, error) {
	sub := map[string]string{}
	varNames := VariablesInText(seg.Text)
	size := CodePointCount(stripVars(seg.Text))
	for _, name := range varNames {
		in := idx[name]
		raw := inputs[name]
		size += rawSizes[name]
		if in.Trust == TrustTrusted {
			sub[name] = raw
		} else {
			sub[name] = WrapUntrusted(name, raw)
		}
	}
	text := Substitute(seg.Text, sub)
	return AssembledPart{Name: seg.Name, Kind: seg.Kind, Text: text, ContentSize: size}, size, nil
}

func renderHistory(seg Segment, history []HistoryEntry) (AssembledPart, int, error) {
	size := 0
	entries := make([]AssembledHistoryEntry, len(history))
	for i, h := range history {
		size += CodePointCount(h.Text)
		entries[i] = AssembledHistoryEntry{
			Role:         h.Role,
			OriginalText: h.Text,
			Text:         WrapUntrusted("history", h.Text),
		}
	}
	return AssembledPart{Name: seg.Name, Kind: KindHistory, History: entries, Text: "", ContentSize: size}, size, nil
}

func firstSegmentOverBudget(t *Template, sizes map[string]int) (name string, size, budget int) {
	for _, seg := range t.Segments {
		if seg.Kind == KindHistory {
			continue
		}
		if seg.Budget > 0 && sizes[seg.Name] > seg.Budget {
			return seg.Name, sizes[seg.Name], seg.Budget
		}
	}
	return "", 0, 0
}

func historyOverBudget(t *Template, sizes map[string]int) bool {
	for _, seg := range t.Segments {
		if seg.Kind != KindHistory || seg.Budget == 0 {
			continue
		}
		return sizes[seg.Name] > seg.Budget
	}
	return false
}

func historySegName(t *Template) string {
	for _, seg := range t.Segments {
		if seg.Kind == KindHistory {
			return seg.Name
		}
	}
	return ""
}

func historyBudget(t *Template) int {
	for _, seg := range t.Segments {
		if seg.Kind == KindHistory {
			return seg.Budget
		}
	}
	return 0
}

func totalSize(sizes map[string]int) int {
	names := make([]string, 0, len(sizes))
	for k := range sizes {
		names = append(names, k)
	}
	sort.Strings(names)
	total := 0
	for _, k := range names {
		total += sizes[k]
	}
	return total
}

func segmentBudgetError(name string, size, budget int) error {
	return core.NewError(core.CodeInvalidArgument, "segment over budget",
		map[string]any{"segment": name, "size": size, "budget": budget})
}

func totalBudgetError(budget, size int) error {
	return core.NewError(core.CodeInvalidArgument, "total over budget",
		map[string]any{"budget": budget, "size": size})
}

func finalizeAssembled(ref string, parts []AssembledPart, dropped int) *Assembled {
	cacheAfter := ""
	for i := range parts {
		if parts[i].Kind == KindStatic || parts[i].Kind == KindSemiStatic {
			cacheAfter = parts[i].Name
		}
	}
	a := &Assembled{
		Ref:            ref,
		Parts:          parts,
		CacheAfter:     cacheAfter,
		HistoryDropped: dropped,
	}
	a.PrefixHash = PrefixHash(parts)
	a.Hash = ContentHash(a)
	return a
}

// TotalContentSize sums segment content sizes (code points of originals, not delimiters).
func TotalContentSize(a *Assembled) int {
	total := 0
	for _, p := range a.Parts {
		total += p.ContentSize
	}
	return total
}

// SegmentContentSize returns the content size of a named part from an assembly.
func SegmentContentSize(a *Assembled, name string) (int, bool) {
	for _, p := range a.Parts {
		if p.Name != name {
			continue
		}
		return p.ContentSize, true
	}
	return 0, false
}

// AssembledText concatenates rendered segment texts in order for comparison.
func AssembledText(a *Assembled) string {
	var b strings.Builder
	for _, p := range a.Parts {
		if p.Kind == KindHistory {
			for _, h := range p.History {
				b.WriteString(h.Role)
				b.WriteString(h.Text)
			}
			continue
		}
		b.WriteString(p.Text)
	}
	return b.String()
}
