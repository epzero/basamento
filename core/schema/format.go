package schema

import js "github.com/santhosh-tekuri/jsonschema/v6"

var assertedFormats = map[string]bool{
	"date-time": true,
	"date":      true,
	"time":      true,
	"duration":  true,
	"email":     true,
	"uri":       true,
	"uuid":      true,
}

// stripNonAssertedFormats disables format validation except for the seven asserted formats.
func stripNonAssertedFormats(s *js.Schema) {
	seen := map[*js.Schema]struct{}{}
	stripFormats(s, seen)
}

func stripFormats(s *js.Schema, seen map[*js.Schema]struct{}) {
	if s == nil {
		return
	}
	if _, ok := seen[s]; ok {
		return
	}
	seen[s] = struct{}{}
	if s.Format != nil && !assertedFormats[s.Format.Name] {
		s.Format = nil
	}
	for _, sub := range s.Properties {
		stripFormats(sub, seen)
	}
	for _, sub := range s.PrefixItems {
		stripFormats(sub, seen)
	}
	if items, ok := s.Items.([]*js.Schema); ok {
		for _, sub := range items {
			stripFormats(sub, seen)
		}
	}
	if sub, ok := s.Items.(*js.Schema); ok {
		stripFormats(sub, seen)
	}
	stripFormats(s.Items2020, seen)
	stripFormats(s.Contains, seen)
	stripFormats(s.Not, seen)
	stripFormats(s.If, seen)
	stripFormats(s.Then, seen)
	stripFormats(s.Else, seen)
	stripFormats(s.PropertyNames, seen)
	for _, sub := range s.AllOf {
		stripFormats(sub, seen)
	}
	for _, sub := range s.AnyOf {
		stripFormats(sub, seen)
	}
	for _, sub := range s.OneOf {
		stripFormats(sub, seen)
	}
	for _, sub := range s.PatternProperties {
		stripFormats(sub, seen)
	}
	for _, sub := range s.DependentSchemas {
		stripFormats(sub, seen)
	}
	if ap, ok := s.AdditionalProperties.(*js.Schema); ok {
		stripFormats(ap, seen)
	}
	stripFormats(s.UnevaluatedProperties, seen)
	stripFormats(s.UnevaluatedItems, seen)
	stripFormats(s.ContentSchema, seen)
	stripFormats(s.Ref, seen)
}
