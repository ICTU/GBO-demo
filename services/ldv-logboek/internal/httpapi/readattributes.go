package httpapi

import (
	"strings"

	"ldv-logboek/internal/ldv"
)

// Translating attributes at the read boundary.
//
// The core standard names attributes flat and snake_case —
// `dpl.core.processing_activity_id` — and that is how a record is written and
// stored. The read extension's schema nests them and spells them camelCase:
//
//	{"dpl": {"core": {"processingActivityId": "…"}}}
//
// Both are normative, each for its own side, so the translation lives here:
// at the boundary between the two, once, rather than being smeared across the
// store or forced onto producers.
//
// Anything the extension does not define travels through unchanged, so a local
// extension's attributes reach a reader rather than being silently dropped.

// readAttributeNames maps a stored flat key onto its path in the read
// extension's nested object.
var readAttributeNames = map[string][]string{
	ldv.AttrProcessingActivityID:      {"dpl", "core", "processingActivityId"},
	ldv.AttrDataSubjectID:             {"dpl", "core", "dataSubjectId"},
	ldv.AttrDataSubjectIDType:         {"dpl", "core", "dataSubjectIdType"},
	ldv.AttrForeignOperationProcessor: {"dpl", "core", "foreignOperation", "processor"},
	ldv.AttrNextLogbookID:             {"dpl", "read", "nextLogbookId"},
}

// toReadAttributes renders stored attributes in the read extension's shape.
func toReadAttributes(stored map[string]any) map[string]any {
	nested := map[string]any{}
	for key, value := range stored {
		if path, known := readAttributeNames[key]; known {
			place(nested, path, value)
			continue
		}
		// Not part of the extension's schema. Nesting it by splitting on dots
		// would invent a structure the sender never wrote, so it travels as
		// the key it is.
		nested[key] = value
	}
	return nested
}

// fromReadAttributes reads selectors out of a request's nested attributes,
// and also accepts the flat spelling — a caller holding a record's own
// attributes should not have to reshape them to ask about it.
func fromReadAttributes(nested map[string]any, storedKey string) string {
	if value, ok := nested[storedKey].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	path, known := readAttributeNames[storedKey]
	if !known {
		return ""
	}
	current := any(nested)
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = object[segment]
		if !ok {
			return ""
		}
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}

// place writes a value at a nested path, creating the objects on the way.
func place(root map[string]any, path []string, value any) {
	current := root
	for _, segment := range path[:len(path)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
	current[path[len(path)-1]] = value
}
