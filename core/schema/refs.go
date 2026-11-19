package schema

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
)

// schemaDataKeywords hold values that are instance data, never subschemas, so
// references inside them are not schema references.
var schemaDataKeywords = map[string]struct{}{
	"const":    {},
	"default":  {},
	"enum":     {},
	"examples": {},
}

// schemaNameMapKeywords hold objects whose member names are arbitrary and
// whose every value is a schema, so a member named "default" or "const" there
// is a property name, not a data keyword.
var schemaNameMapKeywords = map[string]struct{}{
	"$defs":             {},
	"definitions":       {},
	"dependentSchemas":  {},
	"patternProperties": {},
	"properties":        {},
}

func validateRefsClosed(baseID string, raw json.RawMessage, set map[string]json.RawMessage) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return invalidArgf("parse schema: %v", err)
	}
	return walkRefs(baseID, doc, set)
}

func walkRefs(baseID string, v any, set map[string]json.RawMessage) error {
	return walkSchemaRefs(baseID, v, set, false)
}

// walkSchemaRefs walks schema positions. named reports that v is a name map,
// whose keys are member names rather than keywords.
func walkSchemaRefs(baseID string, v any, set map[string]json.RawMessage, named bool) error {
	switch o := v.(type) {
	case map[string]any:
		if !named {
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
		}
		for _, key := range sortedMapKeys(o) {
			if named {
				if err := walkSchemaRefs(baseID, o[key], set, false); err != nil {
					return err
				}
				continue
			}
			if _, skip := schemaDataKeywords[key]; skip {
				continue
			}
			childNamed := false
			if _, isNameMap := schemaNameMapKeywords[key]; isNameMap {
				childNamed = true
			}
			if err := walkSchemaRefs(baseID, o[key], set, childNamed); err != nil {
				return err
			}
		}
	case []any:
		for i := range o {
			if err := walkSchemaRefs(baseID, o[i], set, false); err != nil {
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
