package schema

import (
	"encoding/json"
	"net/url"
	"strings"
)

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
			if strings.HasPrefix(target, "#") {
				return nil
			}
			if _, ok := set[target]; !ok {
				return invalidArg("schema reference not in set", map[string]any{"reference": refName(ref, target)})
			}
		}
		for _, sub := range o {
			if err := walkRefs(baseID, sub, set); err != nil {
				return err
			}
		}
	case []any:
		for _, sub := range o {
			if err := walkRefs(baseID, sub, set); err != nil {
				return err
			}
		}
	}
	return nil
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
