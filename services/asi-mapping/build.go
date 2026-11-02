package asimapping

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Result returns the source's answer for the person: the value at
// result_pointer in the decoded GraphQL response, nil when it is absent or
// null (a person the source does not know).
func (c Capability) Result(response any) any {
	value, _ := lookup(response, c.GraphQL.ResultPointer)
	return value
}

// Build makes the value of the attribute from result, the source's answer
// at result_pointer. With members it makes only those members, for
// fragments; otherwise the whole attribute.
//
// held is false when the source has no value: a required member, or a
// requested member, is missing, or a requested member is a const, which is
// not source data. An error means the source answered a value that does not
// match the attribute's value_schema.
func (a Attribute) Build(result any, schema *ValueSchema, members []string) (value map[string]any, held bool, err error) {
	if result == nil {
		return nil, false, nil
	}
	requested, err := a.requested(members)
	if err != nil {
		return nil, false, err
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
// holds none. An empty string or list is no value.
func (f Field) build(result any) (value any, ok bool, err error) {
	switch {
	case f.Const != nil:
		if err := json.Unmarshal(f.Const, &value); err != nil {
			return nil, false, err
		}
		return value, true, nil
	case f.Each != "":
		list, found := lookup(result, f.Each)
		if !found {
			return nil, false, nil
		}
		elements, isList := list.([]any)
		if !isList {
			return nil, false, fmt.Errorf("%s is not a list", f.Each)
		}
		var values []any
		for _, element := range elements {
			if v, found := lookup(element, f.Pointer); found && !isEmpty(v) {
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
			v, found := lookup(result, pointer)
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
		v, found := lookup(result, f.Pointer)
		if !found || isEmpty(v) {
			return nil, false, nil
		}
		return v, true, nil
	}
}

// lookup follows a JSON Pointer of member names; a missing member or a null
// on the way is not found.
func lookup(document any, pointer string) (any, bool) {
	at := document
	for _, name := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := at.(map[string]any)
		if !ok {
			return nil, false
		}
		if at, ok = object[name]; !ok {
			return nil, false
		}
	}
	return at, at != nil
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

// check reports whether v has the shape of s.
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
		return fmt.Errorf("value %v is not of type %s", v, s.Type)
	}
	return nil
}
