package prompt

import (
	"sort"
	"strings"
)

// LintTemplate checks a template before registration.
func LintTemplate(t *Template) []Finding {
	var out []Finding
	inputTrust := map[string]Trust{}
	for _, in := range t.Inputs {
		if in.Trust == "" {
			inputTrust[in.Name] = TrustUntrusted
		} else {
			inputTrust[in.Name] = in.Trust
		}
	}
	for _, seg := range t.Segments {
		if seg.Kind == KindStatic && len(VariablesInText(seg.Text)) > 0 {
			out = append(out, Finding{Rule: "variable_in_static", Severity: "error", Segment: seg.Name})
		}
		if seg.Kind == KindSemiStatic {
			for _, v := range VariablesInText(seg.Text) {
				if inputTrust[v] != TrustTrusted {
					out = append(out, Finding{Rule: "untrusted_in_cacheable", Severity: "error", Segment: seg.Name})
					break
				}
			}
		}
		if seg.Budget > 0 {
			fixed := stripVars(seg.Text)
			if CodePointCount(fixed) > seg.Budget {
				out = append(out, Finding{Rule: "fixed_text_over_budget", Severity: "error", Segment: seg.Name})
			}
		}
		switch seg.Kind {
		case KindDynamic, KindHistory, KindUser:
			if seg.Budget == 0 {
				out = append(out, Finding{Rule: "no_budget", Severity: "warning", Segment: seg.Name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Segment < out[j].Segment
	})
	return out
}

func stripVars(text string) string {
	return varRef.ReplaceAllString(text, "")
}

// LintAssembled checks delimiter pairing on an assembled prompt.
func LintAssembled(a *Assembled) []Finding {
	var out []Finding
	for _, p := range a.Parts {
		if hasUnpairedDelimiters(p.Text) {
			out = append(out, Finding{Rule: "unpaired_delimiter", Severity: "error", Segment: p.Name})
		}
	}
	return out
}

func hasUnpairedDelimiters(text string) bool {
	depth := 0
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], openDelim) {
			depth++
			i += len(openDelim)
			if j := strings.IndexByte(text[i:], '"'); j >= 0 {
				i += j + 2
			} else {
				return true
			}
			continue
		}
		if strings.HasPrefix(text[i:], closeDelim) {
			depth--
			if depth < 0 {
				return true
			}
			i += len(closeDelim)
			continue
		}
		if strings.HasPrefix(text[i:], "&lt;") {
			i += 4
			continue
		}
		i++
	}
	return depth != 0
}
