package prompt

import "strings"

func lintTemplateStructure(t *Template) []Finding {
	var out []Finding
	if t.ID == "" {
		out = append(out, Finding{Rule: "empty_template_id", Severity: "error"})
	} else if strings.Contains(t.ID, "@") {
		out = append(out, Finding{Rule: "at_in_template_id", Severity: "error"})
	}
	if t.Version == "" {
		out = append(out, Finding{Rule: "empty_template_version", Severity: "error"})
	} else if strings.Contains(t.Version, "@") {
		out = append(out, Finding{Rule: "at_in_template_version", Severity: "error"})
	}
	seenIn := map[string]bool{}
	for _, in := range t.Inputs {
		if seenIn[in.Name] {
			out = append(out, Finding{Rule: "duplicate_input", Severity: "error"})
		}
		seenIn[in.Name] = true
	}
	seenSeg := map[string]bool{}
	hist := 0
	for _, seg := range t.Segments {
		if seenSeg[seg.Name] {
			out = append(out, Finding{Rule: "duplicate_segment", Severity: "error", Segment: seg.Name})
		}
		seenSeg[seg.Name] = true
		if seg.Kind == KindHistory {
			hist++
		}
		if _, ok := kindOrder[seg.Kind]; !ok {
			out = append(out, Finding{Rule: "unknown_segment_kind", Severity: "error", Segment: seg.Name})
		}
	}
	if hist > 1 {
		out = append(out, Finding{Rule: "multiple_history", Severity: "error"})
	}
	return out
}