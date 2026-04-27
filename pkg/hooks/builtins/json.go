package builtins

import (
	"encoding/json"
	"maps"
	"slices"
)

// sortKeys recursively sorts map keys so [json.Marshal] produces
// deterministic output regardless of how the input was constructed.
// Slices are walked in place; non-collection values are returned
// unchanged.
func sortKeys(v any) any {
	switch val := v.(type) {
	case map[string]any:
		sorted := make(map[string]any, len(val))
		for _, k := range slices.Sorted(maps.Keys(val)) {
			sorted[k] = sortKeys(val[k])
		}
		return sorted
	case []any:
		for i, item := range val {
			val[i] = sortKeys(item)
		}
		return val
	default:
		return v
	}
}

// canonicalToolInput returns a stable signature for a tool's input map
// suitable for equality comparison across calls. Marshalling is done
// after a recursive sort so semantically identical maps with different
// iteration orders produce the same bytes. An unmarshalable input or
// an empty map produces an empty string — which the caller should
// treat as a non-matching signature rather than a wildcard.
func canonicalToolInput(in map[string]any) string {
	if len(in) == 0 {
		return ""
	}
	out, err := json.Marshal(sortKeys(in))
	if err != nil {
		return ""
	}
	return string(out)
}
