package prompt

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var varRef = regexp.MustCompile(`\{\{([a-z][a-z0-9_]*)\}\}`)

// VariablesInText returns sorted unique variable names referenced in text.
func VariablesInText(text string) []string {
	seen := map[string]bool{}
	for _, m := range varRef.FindAllStringSubmatch(text, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Substitute replaces {{name}} placeholders in one left-to-right pass; values are not rescanned.
func Substitute(text string, values map[string]string) string {
	out, _ := substitute(text, values, nil)
	return out
}

// substitute is Substitute plus the delimiter records for the names in wrapped.
// The record is taken at the write site, so it names a pair this substitution
// wrote and never one that happens to appear elsewhere in the text.
func substitute(text string, values map[string]string, wrapped map[string]bool) (string, []DelimiterRecord) {
	if len(text) == 0 {
		return "", nil
	}
	var out strings.Builder
	out.Grow(len(text))
	var records []DelimiterRecord
	i := 0
	for i < len(text) {
		if i+1 < len(text) && text[i] == '{' && text[i+1] == '{' {
			rest := text[i+2:]
			end := strings.Index(rest, "}}")
			if end >= 0 {
				name := rest[:end]
				if varRef.MatchString("{{" + name + "}}") {
					if val, ok := values[name]; ok {
						start := out.Len()
						out.WriteString(val)
						if wrapped[name] {
							if rec, ok := delimiterRecordInWrapped(val, start); ok {
								records = append(records, rec)
							}
						}
						i += 2 + end + 2
						continue
					}
				}
			}
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String(), records
}

// CheckDeclaredVars returns the first variable used in segments but not declared.
func CheckDeclaredVars(t *Template) (string, bool) {
	if t == nil {
		return "", false
	}
	decl := map[string]bool{}
	for _, in := range t.Inputs {
		decl[in.Name] = true
	}
	for _, seg := range t.Segments {
		for _, v := range VariablesInText(seg.Text) {
			if !decl[v] {
				return v, false
			}
		}
	}
	return "", true
}

// ParseReference splits id@version; the version part must be non-empty for a valid reference.
func ParseReference(ref string) (id, version string, err error) {
	at := -1
	for i := 0; i < len(ref); i++ {
		if ref[i] == '@' {
			at = i
			break
		}
	}
	if at < 0 {
		return "", "", fmt.Errorf("reference missing version")
	}
	id = ref[:at]
	version = ref[at+1:]
	if id == "" || version == "" {
		return "", "", fmt.Errorf("reference missing version")
	}
	return id, version, nil
}
