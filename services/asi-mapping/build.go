package asimapping

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// typenameField is the GraphQL meta-field that names the type of an answer.
const typenameField = "__typename"

// Result returns the source's answer for the person: the value at
// result_pointer in the decoded GraphQL response, nil when it is absent or
// null (a person the source does not know). An error means the response
// does not have the shape the pointer expects.
func (c Capability) Result(response any) (any, error) {
	value, _, err := lookup(response, c.GraphQL.ResultPointer)
	return value, err
}

// Build makes the value of the attribute from result, the source's answer
// at result_pointer. With members it makes only those members, for
// fragments; otherwise the whole attribute.
//
// held is false when the source has no value: the answer is of another type
// than the attribute's on, a required member, or a requested member, is
// missing, or a requested member is a const, which is not source data. An
// error means the source's answer does not have the shape the mapping and
// the attribute's value_schema expect.
func (a Attribute) Build(result any, schema *ValueSchema, members []string) (value map[string]any, held bool, err error) {
	if result == nil {
		return nil, false, nil
	}
	requested, err := a.requested(members)
	if err != nil {
		return nil, false, err
	}
	if a.On != "" {
		typename, found, err := lookup(result, "/"+typenameField)
		if err != nil {
			return nil, false, fmt.Errorf("attribute %q: %w", a.Identifier, err)
		}
		if !found {
			return nil, false, fmt.Errorf("attribute %q: the answer has no %s", a.Identifier, typenameField)
		}
		if typename != a.On {
			return nil, false, nil
		}
	}
	value = map[string]any{}
	for _, member := range requested {
		field := a.Fields[member]
		if len(members) > 0 && field.Const != nil {
			return nil, false, nil
		}
		v, ok, err := field.build(result)
		if err != nil {
			return nil, false, fmt.Errorf("attribute %q member %q: %w", a.Identifier, member, err)
		}
		if !ok {
			if len(members) > 0 || slices.Contains(schema.Required, member) {
				return nil, false, nil
			}
			continue
		}
		if err := schema.Properties[member].check(v); err != nil {
			return nil, false, fmt.Errorf("attribute %q member %q: %w", a.Identifier, member, err)
		}
		value[member] = v
	}
	return value, true, nil
}

// build returns the member's value from result; ok is false when the source
// holds none. An empty string or list is no value, and a list element
// without a value is left out.
func (f Field) build(result any) (value any, ok bool, err error) {
	switch {
	case f.Const != nil:
		if err := json.Unmarshal(f.Const, &value); err != nil {
			return nil, false, err
		}
		return value, true, nil
	case f.Each != "":
		list, found, err := lookup(result, f.Each)
		if err != nil || !found {
			return nil, false, err
		}
		elements, isList := list.([]any)
		if !isList {
			return nil, false, fmt.Errorf("%s is not a list", f.Each)
		}
		var values []any
		for i, element := range elements {
			v, found, err := lookup(element, f.Pointer)
			if err != nil {
				return nil, false, fmt.Errorf("%s element %d: %w", f.Each, i, err)
			}
			if found && !isEmpty(v) {
				values = append(values, v)
			}
		}
		return values, len(values) > 0, nil
	case len(f.Join) > 0:
		separator := ""
		if f.Separator != nil {
			separator = *f.Separator
		}
		var parts []string
		for _, pointer := range f.Join {
			v, found, err := lookup(result, pointer)
			if err != nil {
				return nil, false, err
			}
			if !found || isEmpty(v) {
				continue
			}
			text, err := asText(v)
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", pointer, err)
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, separator), len(parts) > 0, nil
	default:
		v, found, err := lookup(result, f.Pointer)
		if err != nil || !found || isEmpty(v) {
			return nil, false, err
		}
		return v, true, nil
	}
}

// lookup follows a JSON Pointer of member names; a missing member or a null
// on the way is not found. Any other value on the way is an error: the
// answer does not have the shape the pointer expects.
func lookup(document any, pointer string) (any, bool, error) {
	at := document
	for _, name := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if at == nil {
			return nil, false, nil
		}
		object, ok := at.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("%s does not lead through objects", pointer)
		}
		if at, ok = object[name]; !ok {
			return nil, false, nil
		}
	}
	return at, at != nil, nil
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	}
	return false
}

func asText(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case json.Number:
		return t.String(), nil
	}
	return "", fmt.Errorf("cannot join a %T", v)
}

// dateLayout is RFC 3339 full-date, the JSON Schema format "date".
const dateLayout = "2006-01-02"

// check reports whether v has the shape of s. Its errors name types, not
// values: the value is personal data.
func (s *ValueSchema) check(v any) error {
	switch s.Type {
	case "array":
		items, ok := v.([]any)
		if !ok {
			return fmt.Errorf("value is not a list")
		}
		for i, item := range items {
			if err := s.Items.check(item); err != nil {
				return fmt.Errorf("element %d: %w", i, err)
			}
		}
		return nil
	case "object":
		object, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("value is not an object")
		}
		for name, member := range object {
			if property, ok := s.Properties[name]; ok {
				if err := property.check(member); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
		}
		return nil
	}
	if !hasType(v, s.Type) {
		return fmt.Errorf("a %T is not of type %s", v, s.Type)
	}
	if s.Format == "date" {
		if _, err := time.Parse(dateLayout, v.(string)); err != nil {
			return fmt.Errorf("value is not a date (format date)")
		}
	}
	return nil
}
