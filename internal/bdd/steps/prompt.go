package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/prompt"
)

type promptWorld struct {
	reg            *prompt.Registry
	draft          *prompt.Template
	resolved       *prompt.Template
	lastErr        error
	assembled      []*prompt.Assembled
	regResult      *prompt.RegisterResult
	lintFindings   []prompt.Finding
	mutateAssembly *prompt.Assembled
}

func newPromptWorld() *promptWorld {
	return &promptWorld{reg: prompt.NewRegistry()}
}

func (w *promptWorld) regOrNew() *prompt.Registry {
	if w.reg == nil {
		w.reg = prompt.NewRegistry()
	}
	return w.reg
}

func parseRef(ref string) (id, ver string) {
	id, ver, err := prompt.ParseReference(ref)
	if err != nil {
		parts := strings.SplitN(ref, "@", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
		return ref, ""
	}
	return id, ver
}

func registerSimpleStatic(w *promptWorld, ref, text string) error {
	id, ver := parseRef(ref)
	t := &prompt.Template{
		ID: id, Version: ver,
		Segments: []prompt.Segment{{Name: "main", Kind: prompt.KindStatic, Text: text}},
	}
	_, w.lastErr = w.regOrNew().Register(t)
	return w.lastErr
}

func tableSegmentRows(t *godog.Table) ([]prompt.Segment, error) {
	if len(t.Rows) < 2 {
		return nil, fmt.Errorf("segment table needs header and rows")
	}
	hdr := t.Rows[0].Cells
	col := func(name string) int {
		for i, c := range hdr {
			if c.Value == name {
				return i
			}
		}
		return -1
	}
	segCol, kindCol, textCol, budCol := col("segment"), col("kind"), col("text"), col("budget")
	if segCol < 0 || kindCol < 0 {
		return nil, fmt.Errorf("segment table missing segment or kind column")
	}
	var segs []prompt.Segment
	for _, row := range t.Rows[1:] {
		s := prompt.Segment{
			Name: row.Cells[segCol].Value,
			Kind: prompt.Kind(row.Cells[kindCol].Value),
		}
		if textCol >= 0 && textCol < len(row.Cells) {
			s.Text = row.Cells[textCol].Value
		}
		if budCol >= 0 && budCol < len(row.Cells) {
			b := strings.TrimSpace(row.Cells[budCol].Value)
			if b != "" {
				n, err := strconv.Atoi(b)
				if err != nil {
					return nil, err
				}
				s.Budget = n
			}
		}
		segs = append(segs, s)
	}
	return segs, nil
}

func findingsMatch(got, want []prompt.Finding) error {
	if len(got) != len(want) {
		return fmt.Errorf("got %d findings, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Rule != want[i].Rule || got[i].Severity != want[i].Severity || got[i].Segment != want[i].Segment {
			return fmt.Errorf("finding %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	return nil
}

func findingTable(t *godog.Table) ([]prompt.Finding, error) {
	var out []prompt.Finding
	for _, row := range t.Rows[1:] {
		out = append(out, prompt.Finding{
			Rule:     row.Cells[0].Value,
			Severity: row.Cells[1].Value,
			Segment:  row.Cells[2].Value,
		})
	}
	return out, nil
}

func registerPrompt(sc *godog.ScenarioContext) {
	sc.Step(`^the prompt registry holds template "([^"]*)" with static segment "([^"]*)"$`, func(ctx context.Context, ref, text string) error {
		return registerSimpleStatic(world(ctx).Prompt, ref, text)
	})
	sc.Step(`^a template "([^"]*)" that declares trusted input "([^"]*)" and the segments$`, func(ctx context.Context, ref, in string, table *godog.Table) error {
		w := world(ctx).Prompt
		id, ver := parseRef(ref)
		segs, err := tableSegmentRows(table)
		if err != nil {
			return err
		}
		w.draft = &prompt.Template{
			ID: id, Version: ver,
			Inputs:   []prompt.Input{{Name: in, Trust: prompt.TrustTrusted}},
			Segments: segs,
		}
		return nil
	})
	sc.Step(`^a template "([^"]*)" that declares input "([^"]*)" and the segments$`, func(ctx context.Context, ref, in string, table *godog.Table) error {
		w := world(ctx).Prompt
		id, ver := parseRef(ref)
		segs, err := tableSegmentRows(table)
		if err != nil {
			return err
		}
		w.draft = &prompt.Template{
			ID: id, Version: ver,
			Inputs:   []prompt.Input{{Name: in}},
			Segments: segs,
		}
		return nil
	})
	sc.Step(`^a template "([^"]*)" that declares trusted input "([^"]*)" and input "([^"]*)" and the segments$`, func(ctx context.Context, ref, tIn, in string, table *godog.Table) error {
		w := world(ctx).Prompt
		id, ver := parseRef(ref)
		segs, err := tableSegmentRows(table)
		if err != nil {
			return err
		}
		w.draft = &prompt.Template{
			ID: id, Version: ver,
			Inputs: []prompt.Input{
				{Name: tIn, Trust: prompt.TrustTrusted},
				{Name: in},
			},
			Segments: segs,
		}
		return nil
	})
	sc.Step(`^a template "([^"]*)" that declares input "([^"]*)" and input "([^"]*)" and the segments$`, func(ctx context.Context, ref, in1, in2 string, table *godog.Table) error {
		w := world(ctx).Prompt
		id, ver := parseRef(ref)
		segs, err := tableSegmentRows(table)
		if err != nil {
			return err
		}
		w.draft = &prompt.Template{
			ID: id, Version: ver,
			Inputs:   []prompt.Input{{Name: in1}, {Name: in2}},
			Segments: segs,
		}
		return nil
	})
	sc.Step(`^a template "([^"]*)" with a total budget of (\d+) that declares input "([^"]*)" and the segments$`, func(ctx context.Context, ref string, total int, in string, table *godog.Table) error {
		w := world(ctx).Prompt
		id, ver := parseRef(ref)
		segs, err := tableSegmentRows(table)
		if err != nil {
			return err
		}
		w.draft = &prompt.Template{
			ID: id, Version: ver, TotalBudget: total,
			Inputs:   []prompt.Input{{Name: in}},
			Segments: segs,
		}
		return nil
	})
	sc.Step(`^a template "([^"]*)" that declares trusted input "([^"]*)" and trusted input "([^"]*)" and the segments$`, func(ctx context.Context, ref, in1, in2 string, table *godog.Table) error {
		w := world(ctx).Prompt
		id, ver := parseRef(ref)
		segs, err := tableSegmentRows(table)
		if err != nil {
			return err
		}
		w.draft = &prompt.Template{
			ID: id, Version: ver,
			Inputs: []prompt.Input{
				{Name: in1, Trust: prompt.TrustTrusted},
				{Name: in2, Trust: prompt.TrustTrusted},
			},
			Segments: segs,
		}
		return nil
	})
	sc.Step(`^a template with id "([^"]*)", version "([^"]*)", inputs "([^"]*)" and segments "([^"]*)" each with a budget of (\d+)$`, func(ctx context.Context, id, version, inputs, segments string, budget int) error {
		w := world(ctx).Prompt
		t := &prompt.Template{ID: id, Version: version}
		if s := strings.TrimSpace(inputs); s != "" {
			for _, name := range strings.Split(s, ",") {
				if name = strings.TrimSpace(name); name != "" {
					t.Inputs = append(t.Inputs, prompt.Input{Name: name})
				}
			}
		}
		if s := strings.TrimSpace(segments); s != "" {
			for _, spec := range strings.Split(s, ",") {
				name, kind, ok := strings.Cut(strings.TrimSpace(spec), ":")
				if !ok {
					return fmt.Errorf("segment %q must be name:kind", spec)
				}
				t.Segments = append(t.Segments, prompt.Segment{
					Name:   strings.TrimSpace(name),
					Kind:   prompt.Kind(strings.TrimSpace(kind)),
					Budget: budget,
				})
			}
		}
		w.draft = t
		return nil
	})
	sc.Step(`^the template is registered$`, func(ctx context.Context) error {
		w := world(ctx).Prompt
		w.regResult, w.lastErr = w.regOrNew().Register(w.draft)
		return nil
	})
	sc.Step(`^template "([^"]*)" is registered with static segment "([^"]*)"$`, func(ctx context.Context, ref, text string) error {
		w := world(ctx).Prompt
		id, ver := parseRef(ref)
		t := &prompt.Template{
			ID: id, Version: ver,
			Segments: []prompt.Segment{{Name: "main", Kind: prompt.KindStatic, Text: text}},
		}
		w.regResult, w.lastErr = w.regOrNew().Register(t)
		return nil
	})
	sc.Step(`^"([^"]*)" is resolved$`, func(ctx context.Context, ref string) error {
		w := world(ctx).Prompt
		w.resolved, w.lastErr = w.regOrNew().Resolve(ref)
		return nil
	})
	sc.Step(`^"([^"]*)" is assembled with inputs (\{.*\})$`, func(ctx context.Context, ref, raw string) error {
		return assembleWith(ctx, ref, raw, nil)
	})
	sc.Step(`^"([^"]*)" is assembled with inputs (\{.*\}) and no history$`, func(ctx context.Context, ref, raw string) error {
		return assembleWith(ctx, ref, raw, nil)
	})
	sc.Step(`^"([^"]*)" is assembled again with inputs (\{.*\})$`, func(ctx context.Context, ref, raw string) error {
		return assembleWith(ctx, ref, raw, nil)
	})
	sc.Step(`^"([^"]*)" is assembled again with inputs (\{.*\}) and no history$`, func(ctx context.Context, ref, raw string) error {
		return assembleWith(ctx, ref, raw, nil)
	})
	sc.Step(`^"([^"]*)" is assembled again with inputs (\{.*\}) and (\d+) history entries$`, func(ctx context.Context, ref, raw string, n int) error {
		hist := make([]prompt.HistoryEntry, n)
		for i := range hist {
			hist[i] = prompt.HistoryEntry{Role: "user", Text: fmt.Sprintf("h%d", i)}
		}
		return assembleWith(ctx, ref, raw, hist)
	})
	sc.Step(`^"([^"]*)" is assembled with inputs (\{.*\}) and the history$`, func(ctx context.Context, ref, raw string, table *godog.Table) error {
		hist, err := historyTable(table)
		if err != nil {
			return err
		}
		return assembleWith(ctx, ref, raw, hist)
	})
	sc.Step(`^"([^"]*)" was assembled with inputs (\{.*\})$`, func(ctx context.Context, ref, raw string) error {
		return assembleWith(ctx, ref, raw, nil)
	})
	sc.Step(`^the template is linted$`, func(ctx context.Context) error {
		world(ctx).Prompt.lintFindings = prompt.LintTemplate(world(ctx).Prompt.draft)
		return nil
	})
	sc.Step(`^the assembled prompt is linted$`, func(ctx context.Context) error {
		a := world(ctx).Prompt.mutateAssembly
		if a == nil && len(world(ctx).Prompt.assembled) > 0 {
			a = world(ctx).Prompt.assembled[len(world(ctx).Prompt.assembled)-1]
		}
		world(ctx).Prompt.lintFindings = prompt.LintAssembled(a)
		return nil
	})
	sc.Step(`^the assembled segment "([^"]*)" is changed afterwards by appending "([^"]*)"$`, func(ctx context.Context, name, suffix string) error {
		w := world(ctx).Prompt
		if len(w.assembled) == 0 {
			return fmt.Errorf("no assembly to mutate")
		}
		cp := cloneAssembledForMutation(w.assembled[len(w.assembled)-1])
		for i := range cp.Parts {
			if cp.Parts[i].Name == name {
				cp.Parts[i].Text += suffix
			}
		}
		w.mutateAssembly = cp
		return nil
	})
	sc.Step(`^the assembled segment "([^"]*)" is changed afterwards by removing "([^"]*)"$`, func(ctx context.Context, name, fragment string) error {
		w := world(ctx).Prompt
		if len(w.assembled) == 0 {
			return fmt.Errorf("no assembly to mutate")
		}
		cp := cloneAssembledForMutation(w.assembled[len(w.assembled)-1])
		for i := range cp.Parts {
			if cp.Parts[i].Name == name {
				cp.Parts[i].Text = strings.Replace(cp.Parts[i].Text, fragment, "", 1)
			}
		}
		w.mutateAssembly = cp
		return nil
	})

	// Then steps
	sc.Step(`^the resolved template has static segment "([^"]*)"$`, func(ctx context.Context, want string) error {
		t := world(ctx).Prompt.resolved
		if t == nil {
			return fmt.Errorf("no resolved template")
		}
		for _, s := range t.Segments {
			if s.Kind == prompt.KindStatic && s.Text == want {
				return nil
			}
		}
		return fmt.Errorf("no static segment %q", want)
	})
	sc.Step(`^"([^"]*)" resolves to a template with static segment "([^"]*)"$`, func(ctx context.Context, ref, want string) error {
		t, err := world(ctx).Prompt.regOrNew().Resolve(ref)
		if err != nil {
			return err
		}
		for _, s := range t.Segments {
			if s.Kind == prompt.KindStatic && s.Text == want {
				return nil
			}
		}
		return fmt.Errorf("no static segment %q", want)
	})
	sc.Step(`^the registration fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return expectCode(world(ctx).Prompt.lastErr, code)
	})
	sc.Step(`^the registration succeeds$`, func(ctx context.Context) error {
		if world(ctx).Prompt.lastErr != nil {
			return world(ctx).Prompt.lastErr
		}
		return nil
	})
	sc.Step(`^the prompt registry holds (\d+) version of template "([^"]*)"$`, func(ctx context.Context, n int, id string) error {
		got := world(ctx).Prompt.regOrNew().VersionCount(id)
		if got != n {
			return fmt.Errorf("got %d versions, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the prompt registry holds (\d+) versions of template "([^"]*)"$`, func(ctx context.Context, n int, id string) error {
		got := world(ctx).Prompt.regOrNew().VersionCount(id)
		if got != n {
			return fmt.Errorf("got %d versions, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the resolution fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return expectCode(world(ctx).Prompt.lastErr, code)
	})
	sc.Step(`^the registration fails with code "([^"]*)" naming variable "([^"]*)"$`, func(ctx context.Context, code, name string) error {
		if err := expectCode(world(ctx).Prompt.lastErr, code); err != nil {
			return err
		}
		return expectDetail(world(ctx).Prompt.lastErr, "variable", name)
	})
	sc.Step(`^"([^"]*)" does not resolve$`, func(ctx context.Context, ref string) error {
		_, err := world(ctx).Prompt.regOrNew().Resolve(ref)
		if err == nil {
			return fmt.Errorf("expected not found")
		}
		return nil
	})
	sc.Step(`^the assembly fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return expectCode(world(ctx).Prompt.lastErr, code)
	})
	sc.Step(`^the error does not contain "([^"]*)"$`, func(ctx context.Context, fragment string) error {
		err := world(ctx).Prompt.lastErr
		if err == nil {
			return fmt.Errorf("no error to inspect")
		}
		if strings.Contains(err.Error(), fragment) {
			return fmt.Errorf("error %q contains forbidden fragment %q", err.Error(), fragment)
		}
		return nil
	})
	sc.Step(`^the assembly fails with code "([^"]*)" naming input "([^"]*)"$`, func(ctx context.Context, code, name string) error {
		if err := expectCode(world(ctx).Prompt.lastErr, code); err != nil {
			return err
		}
		return expectDetail(world(ctx).Prompt.lastErr, "input", name)
	})
	sc.Step(`^segment "([^"]*)" reads$`, func(ctx context.Context, name string, doc *godog.DocString) error {
		return segmentText(ctx, name, strings.TrimSpace(doc.Content))
	})
	sc.Step(`^segment "([^"]*)" reads "([^"]*)"$`, segmentText)
	sc.Step(`^both assembled prompts have the same text and the same content hash$`, func(ctx context.Context) error {
		w := world(ctx).Prompt
		if len(w.assembled) < 2 {
			return fmt.Errorf("need two assemblies")
		}
		a, b := w.assembled[len(w.assembled)-2], w.assembled[len(w.assembled)-1]
		if prompt.AssembledText(a) != prompt.AssembledText(b) {
			return fmt.Errorf("text differs")
		}
		if a.Hash != b.Hash {
			return fmt.Errorf("hash differs")
		}
		return nil
	})
	sc.Step(`^the assembled segments are, in order, (.+)$`, func(ctx context.Context, list string) error {
		list = strings.TrimPrefix(list, `"`)
		parts := strings.Split(list, `", "`)
		for i := range parts {
			parts[i] = strings.Trim(parts[i], `"`)
		}
		w := world(ctx).Prompt
		if len(w.assembled) == 0 {
			return fmt.Errorf("no assembly")
		}
		last := w.assembled[len(w.assembled)-1]
		if len(last.Parts) != len(parts) {
			return fmt.Errorf("got %d segments, want %v", len(last.Parts), parts)
		}
		for i, name := range parts {
			if last.Parts[i].Name != name {
				return fmt.Errorf("segment %d: got %q want %q", i, last.Parts[i].Name, name)
			}
		}
		return nil
	})
	sc.Step(`^the assembly fails with code "([^"]*)" naming segment "([^"]*)" with size (\d+) and budget (\d+)$`, func(ctx context.Context, code, seg string, size, budget int) error {
		if err := expectCode(world(ctx).Prompt.lastErr, code); err != nil {
			return err
		}
		return expectDetails(world(ctx).Prompt.lastErr, map[string]any{"segment": seg, "size": size, "budget": budget})
	})
	sc.Step(`^the assembly succeeds with segment "([^"]*)" of size (\d+)$`, func(ctx context.Context, name string, size int) error {
		if world(ctx).Prompt.lastErr != nil {
			return world(ctx).Prompt.lastErr
		}
		w := world(ctx).Prompt
		if len(w.assembled) == 0 {
			return fmt.Errorf("no assembly")
		}
		got, ok := prompt.SegmentContentSize(w.assembled[len(w.assembled)-1], name)
		if !ok || got != size {
			return fmt.Errorf("segment %q size %d, want %d", name, got, size)
		}
		return nil
	})
	sc.Step(`^the history segment holds the entries "([^"]*)", "([^"]*)"(?:, "([^"]*)")?$`, func(ctx context.Context, e1, e2, e3 string) error {
		names := []string{e1, e2}
		if e3 != "" {
			names = append(names, e3)
		}
		return historyEntries(ctx, names)
	})
	sc.Step(`^the assembled prompt reports (\d+) history entr(?:y|ies) dropped$`, func(ctx context.Context, n int) error {
		w := world(ctx).Prompt
		if len(w.assembled) == 0 {
			return fmt.Errorf("no assembly")
		}
		got := w.assembled[len(w.assembled)-1].HistoryDropped
		if got != n {
			return fmt.Errorf("dropped %d, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the assembled prompt has size (\d+) and reports 1 history entry dropped$`, func(ctx context.Context, size int) error {
		w := world(ctx).Prompt
		if len(w.assembled) == 0 {
			return fmt.Errorf("no assembly")
		}
		last := w.assembled[len(w.assembled)-1]
		if prompt.TotalContentSize(last) != size {
			return fmt.Errorf("size %d want %d", prompt.TotalContentSize(last), size)
		}
		if last.HistoryDropped != 1 {
			return fmt.Errorf("dropped %d", last.HistoryDropped)
		}
		return nil
	})
	sc.Step(`^the assembly fails with code "([^"]*)" naming the total budget (\d+) and size (\d+)$`, func(ctx context.Context, code string, budget, size int) error {
		if err := expectCode(world(ctx).Prompt.lastErr, code); err != nil {
			return err
		}
		return expectDetails(world(ctx).Prompt.lastErr, map[string]any{"budget": budget, "size": size})
	})
	sc.Step(`^the assembled prompt has (\d+) cache hint(?:s)?, after segment "([^"]*)"$`, func(ctx context.Context, n int, seg string) error {
		w := world(ctx).Prompt
		if len(w.assembled) == 0 {
			return fmt.Errorf("no assembly")
		}
		last := w.assembled[len(w.assembled)-1]
		if n == 1 && last.CacheAfter != seg {
			return fmt.Errorf("cache after %q, want %q", last.CacheAfter, seg)
		}
		return nil
	})
	sc.Step(`^the assembled prompt has 0 cache hints and an empty prefix hash$`, func(ctx context.Context) error {
		last := world(ctx).Prompt.assembled[len(world(ctx).Prompt.assembled)-1]
		if last.CacheAfter != "" || last.PrefixHash != "" {
			return fmt.Errorf("cache=%q prefix=%q", last.CacheAfter, last.PrefixHash)
		}
		return nil
	})
	sc.Step(`^both assembled prompts have the same prefix hash and different content hashes$`, func(ctx context.Context) error {
		w := world(ctx).Prompt
		a, b := w.assembled[len(w.assembled)-2], w.assembled[len(w.assembled)-1]
		if a.PrefixHash != b.PrefixHash || a.Hash == b.Hash {
			return fmt.Errorf("prefix/hash mismatch")
		}
		return nil
	})
	sc.Step(`^the two assembled prompts have different prefix hashes$`, func(ctx context.Context) error {
		w := world(ctx).Prompt
		a, b := w.assembled[len(w.assembled)-2], w.assembled[len(w.assembled)-1]
		if a.PrefixHash == b.PrefixHash {
			return fmt.Errorf("same prefix hash")
		}
		return nil
	})
	sc.Step(`^prompt lint reports exactly the findings$`, func(ctx context.Context, table *godog.Table) error {
		want, err := findingTable(table)
		if err != nil {
			return err
		}
		return findingsMatch(world(ctx).Prompt.lintFindings, want)
	})
	sc.Step(`^prompt lint reports no findings$`, func(ctx context.Context) error {
		if len(world(ctx).Prompt.lintFindings) != 0 {
			return fmt.Errorf("got findings %+v", world(ctx).Prompt.lintFindings)
		}
		return nil
	})
	sc.Step(`^the registration fails with code "([^"]*)" listing the lint error "([^"]*)" for segment "([^"]*)"$`, func(ctx context.Context, code, rule, seg string) error {
		if err := expectCode(world(ctx).Prompt.lastErr, code); err != nil {
			return err
		}
		return lintListed(world(ctx).Prompt.lastErr, rule, seg)
	})
	sc.Step(`^the registration fails with code "([^"]*)" listing the lint error "([^"]*)"$`, func(ctx context.Context, code, rule string) error {
		if err := expectCode(world(ctx).Prompt.lastErr, code); err != nil {
			return err
		}
		return lintRuleListed(world(ctx).Prompt.lastErr, rule)
	})
	sc.Step(`^the registration succeeds with the lint warning "([^"]*)" for segment "([^"]*)"$`, func(ctx context.Context, rule, seg string) error {
		if world(ctx).Prompt.lastErr != nil {
			return world(ctx).Prompt.lastErr
		}
		r := world(ctx).Prompt.regResult
		if r == nil {
			return fmt.Errorf("no register result")
		}
		for _, f := range r.Warnings {
			if f.Rule == rule && f.Segment == seg {
				return nil
			}
		}
		return fmt.Errorf("warning %q for %q not returned", rule, seg)
	})
	sc.Step(`^"([^"]*)" resolves$`, func(ctx context.Context, ref string) error {
		_, err := world(ctx).Prompt.regOrNew().Resolve(ref)
		return err
	})
	sc.Step(`^the history segment holds (\d+) entr(?:y|ies), each wrapped once with source "([^"]*)"$`, historyWrapped)
	sc.Step(`^segment "([^"]*)" holds (\d+) value wrapped with source "([^"]*)"$`, func(ctx context.Context, name string, n int, source string) error {
		part, err := findPart(ctx, name)
		if err != nil {
			return err
		}
		o, c := prompt.CountUntrustedDelimiters(part.Text)
		if n != 1 || o != 1 || c != 1 {
			return fmt.Errorf("delimiter count o=%d c=%d", o, c)
		}
		want := `source="` + source + `"`
		if !strings.Contains(part.Text, want) {
			return fmt.Errorf("missing source %q", source)
		}
		return nil
	})
	sc.Step(`^segment "([^"]*)" contains exactly 1 opening and 1 closing untrusted-content delimiter$`, func(ctx context.Context, name string) error {
		part, err := findPart(ctx, name)
		if err != nil {
			return err
		}
		o, c := prompt.CountUntrustedDelimiters(part.Text)
		if o != 1 || c != 1 {
			return fmt.Errorf("opens=%d closes=%d", o, c)
		}
		return nil
	})
}

func assembleWith(ctx context.Context, ref, raw string, hist []prompt.HistoryEntry) error {
	w := world(ctx).Prompt
	var inputs map[string]string
	if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
		return err
	}
	a, err := w.regOrNew().Assemble(ref, inputs, hist)
	w.lastErr = err
	if err == nil {
		w.assembled = append(w.assembled, a)
	}
	return nil
}

func historyTable(t *godog.Table) ([]prompt.HistoryEntry, error) {
	var out []prompt.HistoryEntry
	for _, row := range t.Rows[1:] {
		out = append(out, prompt.HistoryEntry{Role: row.Cells[0].Value, Text: row.Cells[1].Value})
	}
	return out, nil
}

func expectCode(err error, code string) error {
	if err == nil {
		return fmt.Errorf("succeeded, want code %s", code)
	}
	if string(core.CodeOf(err)) != code {
		return fmt.Errorf("code %s, want %s", core.CodeOf(err), code)
	}
	return nil
}

func expectDetail(err error, key, want string) error {
	var pe *core.Error
	if !errorsAs(err, &pe) {
		return fmt.Errorf("not a core error")
	}
	v, ok := pe.Details[key]
	if !ok || fmt.Sprint(v) != want {
		return fmt.Errorf("detail %s=%v, want %s", key, v, want)
	}
	return nil
}

func expectDetails(err error, want map[string]any) error {
	var pe *core.Error
	if !errorsAs(err, &pe) {
		return fmt.Errorf("not a core error")
	}
	for k, v := range want {
		got, ok := pe.Details[k]
		if !ok || fmt.Sprint(got) != fmt.Sprint(v) {
			return fmt.Errorf("detail %s=%v, want %v", k, got, v)
		}
	}
	return nil
}

func cloneAssembledForMutation(a *prompt.Assembled) *prompt.Assembled {
	cp := *a
	cp.Parts = append([]prompt.AssembledPart(nil), a.Parts...)
	for i := range cp.Parts {
		cp.Parts[i].Wrapped = append([]prompt.DelimiterRecord(nil), a.Parts[i].Wrapped...)
		if len(a.Parts[i].History) > 0 {
			cp.Parts[i].History = append([]prompt.AssembledHistoryEntry(nil), a.Parts[i].History...)
			for j := range cp.Parts[i].History {
				cp.Parts[i].History[j].Wrapped = append([]prompt.DelimiterRecord(nil), a.Parts[i].History[j].Wrapped...)
			}
		}
	}
	return &cp
}

func lintRuleListed(err error, rule string) error {
	var pe *core.Error
	if !errorsAs(err, &pe) {
		return fmt.Errorf("not a core error")
	}
	raw, ok := pe.Details["lint"]
	if !ok {
		return fmt.Errorf("no lint details")
	}
	slice, ok := raw.([]map[string]string)
	if !ok {
		return fmt.Errorf("unexpected lint detail type")
	}
	for _, item := range slice {
		if item["rule"] == rule {
			return nil
		}
	}
	return fmt.Errorf("lint %q not listed", rule)
}

func lintListed(err error, rule, seg string) error {
	var pe *core.Error
	if !errorsAs(err, &pe) {
		return fmt.Errorf("not a core error")
	}
	raw, ok := pe.Details["lint"]
	if !ok {
		return fmt.Errorf("no lint details")
	}
	slice, ok := raw.([]map[string]string)
	if !ok {
		return fmt.Errorf("unexpected lint detail type")
	}
	for _, item := range slice {
		if item["rule"] == rule && item["segment"] == seg {
			return nil
		}
	}
	return fmt.Errorf("lint %q/%q not listed", rule, seg)
}

func segmentText(ctx context.Context, name, want string) error {
	part, err := findPart(ctx, name)
	if err != nil {
		return err
	}
	if part.Text != want {
		return fmt.Errorf("got %q", part.Text)
	}
	return nil
}

func findPart(ctx context.Context, name string) (prompt.AssembledPart, error) {
	w := world(ctx).Prompt
	if len(w.assembled) == 0 {
		return prompt.AssembledPart{}, fmt.Errorf("no assembly")
	}
	last := w.assembled[len(w.assembled)-1]
	for _, p := range last.Parts {
		if p.Name == name {
			return p, nil
		}
	}
	return prompt.AssembledPart{}, fmt.Errorf("segment %q not found", name)
}

func historyEntries(ctx context.Context, texts []string) error {
	part, err := findPartByKind(ctx, prompt.KindHistory)
	if err != nil {
		return err
	}
	if len(part.History) != len(texts) {
		return fmt.Errorf("got %d entries, want %d", len(part.History), len(texts))
	}
	for i, want := range texts {
		if part.History[i].OriginalText != want {
			return fmt.Errorf("entry %d: got %q", i, part.History[i].OriginalText)
		}
	}
	return nil
}

func historyWrapped(ctx context.Context, n int, source string) error {
	part, err := findPartByKind(ctx, prompt.KindHistory)
	if err != nil {
		return err
	}
	if len(part.History) != n {
		return fmt.Errorf("got %d entries", len(part.History))
	}
	want := `source="` + source + `"`
	for i, h := range part.History {
		o, c := prompt.CountUntrustedDelimiters(h.Text)
		if o != 1 || c != 1 || !strings.Contains(h.Text, want) {
			return fmt.Errorf("entry %d bad wrap", i)
		}
	}
	return nil
}

func findPartByKind(ctx context.Context, kind prompt.Kind) (prompt.AssembledPart, error) {
	w := world(ctx).Prompt
	last := w.assembled[len(w.assembled)-1]
	for _, p := range last.Parts {
		if p.Kind == kind {
			return p, nil
		}
	}
	return prompt.AssembledPart{}, fmt.Errorf("no %s segment", kind)
}

func errorsAs(err error, target **core.Error) bool {
	return errors.As(err, target)
}
