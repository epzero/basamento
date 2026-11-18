package schema

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
)

var schemaDataKeywords = map[string]struct{}{
	"const":    {},
	"default":  {},
	"examples": {},
}

func validateRefsClosed(baseID string, raw json.RawMessage, set map[string]json.RawMessage) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return invalidArgf("parse schema: %v", err)
	}
	return walkRefs(baseID, doc, set)
}

func walkRefs(baseID string, v any, set map[string]json.RawMessage) error {
	switch o := v.(type) {
	case map[string]any:
		if ref, ok := o["$ref"].(string); ok {
			target, err := resolveRef(baseID, ref)
			if err != nil {
				return invalidArgf("resolve reference: %v", err)
			}
			docID := stripFragment(target)
			if docID != "" && !strings.HasPrefix(ref, "#") {
				if _, ok := set[docID]; !ok {
					return invalidArg("schema reference not in set", map[string]any{"reference": refName(ref, target)})
				}
			}
		}
		for _, key := range sortedMapKeys(o) {
			if _, skip := schemaDataKeywords[key]; skip {
				continue
			}
			if err := walkRefs(baseID, o[key], set); err != nil {
				return err
			}
		}
	case []any:
		for i := range o {
			if err := walkRefs(baseID, o[i], set); err != nil {
				return err
			}
		}
	}
	return nil
}

func stripFragment(u string) string {
	if i := strings.Index(u, "#"); i >= 0 {
		return u[:i]
	}
	return u
}

func resolveRef(base, ref string) (string, error) {
	if strings.HasPrefix(ref, "#") {
		return ref, nil
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return baseURL.ResolveReference(refURL).String(), nil
}

func refName(original, _ string) string {
	return original
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
