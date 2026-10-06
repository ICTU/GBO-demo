package ftvgraphql

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// variable is the coerced value of one variable of the selected operation.
// Only variables that were supplied or have a declared default have one.
type variable struct {
	value     any
	defaulted bool    // resolved to the declared default
	defaults  [][]any // input fields inside value filled from a schema default
}

// coerceVariables follows CoerceVariableValues of the GraphQL specification.
// Built-in scalars, enums, lists, input objects and non-null are coerced
// strictly; a custom scalar passes through as received, because the
// source's coercion of it is unknown.
func coerceVariables(schema *ast.Schema, op *ast.OperationDefinition, supplied map[string]any) (map[string]variable, *Unverifiable) {
	vars := map[string]variable{}
	for _, def := range op.VariableDefinitions {
		c := &coercer{schema: schema, record: true}
		raw, has := supplied[def.Variable]
		switch {
		case has:
			v, err := c.input(def.Type, raw, nil)
			if err != nil {
				return nil, unverifiable(SubVariableError, "variable $%s: %s", def.Variable, err)
			}
			vars[def.Variable] = variable{value: v, defaults: c.defaults}
		case def.DefaultValue != nil:
			v, _, err := c.literal(def.Type, def.DefaultValue, nil)
			if err != nil {
				return nil, unverifiable(SubVariableError, "variable $%s default: %s", def.Variable, err)
			}
			vars[def.Variable] = variable{value: v, defaulted: true, defaults: c.defaults}
		case def.Type.NonNull:
			return nil, unverifiable(SubVariableError, "variable $%s: non-null and not supplied", def.Variable)
		}
	}
	return vars, nil
}

// variableError is a coercion failure caused by the runtime value of a
// variable rather than by the document: VARIABLE_ERROR, not INVALID_QUERY.
type variableError struct{ error }

func isVariableError(err error) bool {
	var v variableError
	return errors.As(err, &v)
}

// isOneOf reports a OneOf Input Object: exactly one field set, and not null.
func isOneOf(def *ast.Definition) bool {
	return def.Directives.ForName("oneOf") != nil
}

// argument resolves one argument of a selection (Section 6.5). ok is false
// when the argument has no value: not written, or written as a variable
// that has none, and without a schema default.
func argument(schema *ast.Schema, vars map[string]variable, def *ast.ArgumentDefinition, written *ast.Value) (Arg, bool, error) {
	if written != nil {
		c := &coercer{schema: schema, vars: vars, record: true}
		v, present, err := c.literal(def.Type, written, nil)
		if err != nil {
			return Arg{}, false, err
		}
		if present {
			arg := Arg{Value: v, Origin: c.origin(written), Variables: c.used, SchemaDefaults: c.defaults}
			return arg, true, nil
		}
	}
	if def.DefaultValue == nil {
		return Arg{}, false, nil
	}
	c := &coercer{schema: schema}
	v, _, err := c.literal(def.Type, def.DefaultValue, nil)
	if err != nil {
		return Arg{}, false, err
	}
	return Arg{Value: v, Origin: "schema-default"}, true, nil
}

// coercer coerces one value and records what Arg reports about it.
type coercer struct {
	schema *ast.Schema
	vars   map[string]variable
	// record is false while coercing a schema default as a whole: its
	// parts are not reported separately.
	record   bool
	used     []string // variables referenced, in document order
	defaults [][]any  // paths filled from an input-field default
}

func (c *coercer) origin(written *ast.Value) string {
	if written.Kind == ast.Variable {
		if c.vars[written.Raw].defaulted {
			return "default:" + written.Raw
		}
		return "variable:" + written.Raw
	}
	if len(c.used) == 0 {
		return "literal"
	}
	return "mixed"
}

func (c *coercer) useVariable(name string, path []any) (variable, bool) {
	if !slices.Contains(c.used, name) {
		c.used = append(c.used, name)
	}
	v, ok := c.vars[name]
	if ok && c.record {
		for _, p := range v.defaults {
			c.defaults = append(c.defaults, join(path, p...))
		}
	}
	return v, ok
}

// literal coerces a value from the document. present is false for a
// variable without a value: the caller decides what that means in its
// position.
func (c *coercer) literal(t *ast.Type, v *ast.Value, path []any) (any, bool, error) {
	if v.Kind == ast.Variable {
		vv, ok := c.useVariable(v.Raw, path)
		// A nullable variable with a default may stand in a non-null
		// position; a null supplied for it is then an error.
		if ok && vv.value == nil && t.NonNull {
			return nil, false, variableError{fmt.Errorf("variable $%s is null in a non-null position", v.Raw)}
		}
		return vv.value, ok, nil
	}
	if v.Kind == ast.NullValue {
		if t.NonNull {
			return nil, false, fmt.Errorf("null for non-null %s", t)
		}
		return nil, true, nil
	}
	if t.Elem != nil {
		return c.listLiteral(t, v, path)
	}
	def := c.schema.Types[t.NamedType]
	if def == nil {
		return nil, false, fmt.Errorf("unknown type %s", t.NamedType)
	}
	switch def.Kind {
	case ast.Scalar:
		val, err := c.scalarLiteral(def.Name, v, path)
		return val, err == nil, err
	case ast.Enum:
		if v.Kind != ast.EnumValue || def.EnumValues.ForName(v.Raw) == nil {
			return nil, false, fmt.Errorf("not a value of enum %s", def.Name)
		}
		return v.Raw, true, nil
	case ast.InputObject:
		if v.Kind != ast.ObjectValue {
			return nil, false, fmt.Errorf("not an input object %s", def.Name)
		}
		if isOneOf(def) && (len(v.Children) != 1 || v.Children[0].Value.Kind == ast.NullValue) {
			return nil, false, fmt.Errorf("OneOf Input Object %s needs exactly one non-null field", def.Name)
		}
		obj, err := c.inputObject(def, func(name string) (any, bool, error) {
			written := v.Children.ForName(name)
			if written == nil {
				return nil, false, nil
			}
			return c.literal(def.Fields.ForName(name).Type, written, join(path, name))
		}, path)
		if err == nil && isOneOf(def) && !oneEntryNotNull(obj) {
			// The one field was a variable without a value, or null.
			err = variableError{fmt.Errorf("OneOf Input Object %s needs exactly one non-null field", def.Name)}
		}
		return obj, err == nil, err
	}
	return nil, false, fmt.Errorf("%s is not an input type", def.Name)
}

func (c *coercer) listLiteral(t *ast.Type, v *ast.Value, path []any) (any, bool, error) {
	if v.Kind != ast.ListValue {
		// Input coercion of a single value where a list is expected.
		item, present, err := c.literal(t.Elem, v, join(path, 0))
		if err != nil || !present {
			return nil, present, err
		}
		return []any{item}, true, nil
	}
	list := make([]any, 0, len(v.Children))
	for i, child := range v.Children {
		item, present, err := c.literal(t.Elem, child.Value, join(path, i))
		if err != nil {
			return nil, false, err
		}
		if !present {
			// A list item that is a variable without a value is null.
			if t.Elem.NonNull {
				return nil, false, fmt.Errorf("null item for non-null %s", t.Elem)
			}
			item = nil
		}
		list = append(list, item)
	}
	return list, true, nil
}

// inputObject builds an input object field by field. field reports the
// written (or supplied) value of one field; a field without one takes its
// default, which is recorded in schemaDefaults.
func (c *coercer) inputObject(def *ast.Definition, field func(string) (any, bool, error), path []any) (map[string]any, error) {
	obj := map[string]any{}
	for _, fd := range def.Fields {
		v, present, err := field(fd.Name)
		if err != nil {
			return nil, err
		}
		if present {
			obj[fd.Name] = v
			continue
		}
		if fd.DefaultValue == nil {
			if fd.Type.NonNull {
				return nil, fmt.Errorf("input field %s.%s is required", def.Name, fd.Name)
			}
			continue
		}
		whole := &coercer{schema: c.schema}
		dv, _, err := whole.literal(fd.Type, fd.DefaultValue, nil)
		if err != nil {
			return nil, err
		}
		obj[fd.Name] = dv
		if c.record {
			c.defaults = append(c.defaults, join(path, fd.Name))
		}
	}
	return obj, nil
}

func (c *coercer) scalarLiteral(name string, v *ast.Value, path []any) (any, error) {
	switch name {
	case "Int":
		if v.Kind == ast.IntValue {
			if n, err := strconv.ParseInt(v.Raw, 10, 32); err == nil {
				return json.Number(strconv.FormatInt(n, 10)), nil
			}
		}
	case "Float":
		if v.Kind == ast.IntValue || v.Kind == ast.FloatValue {
			if f, err := strconv.ParseFloat(v.Raw, 64); err == nil && !math.IsInf(f, 0) {
				return json.Number(v.Raw), nil
			}
		}
	case "String":
		if v.Kind == ast.StringValue || v.Kind == ast.BlockValue {
			return v.Raw, nil
		}
	case "Boolean":
		if v.Kind == ast.BooleanValue {
			return v.Raw == "true", nil
		}
	case "ID":
		if v.Kind == ast.StringValue || v.Kind == ast.IntValue {
			return v.Raw, nil
		}
	default:
		return c.untyped(v, path), nil
	}
	return nil, fmt.Errorf("not a valid %s", name)
}

// untyped converts the literal of a custom scalar to JSON as written.
func (c *coercer) untyped(v *ast.Value, path []any) any {
	switch v.Kind {
	case ast.Variable:
		vv, _ := c.useVariable(v.Raw, path)
		return vv.value
	case ast.IntValue, ast.FloatValue:
		return json.Number(v.Raw)
	case ast.BooleanValue:
		return v.Raw == "true"
	case ast.NullValue:
		return nil
	case ast.ListValue:
		list := make([]any, 0, len(v.Children))
		for i, child := range v.Children {
			list = append(list, c.untyped(child.Value, join(path, i)))
		}
		return list
	case ast.ObjectValue:
		obj := map[string]any{}
		for _, child := range v.Children {
			if child.Value.Kind == ast.Variable {
				if _, ok := c.vars[child.Value.Raw]; !ok {
					c.useVariable(child.Value.Raw, path)
					continue
				}
			}
			obj[child.Name] = c.untyped(child.Value, join(path, child.Name))
		}
		return obj
	default: // String, Block, Enum
		return v.Raw
	}
}

// input coerces a supplied variable value, as decoded from JSON.
func (c *coercer) input(t *ast.Type, v any, path []any) (any, error) {
	if v == nil {
		if t.NonNull {
			return nil, fmt.Errorf("null for non-null %s", t)
		}
		return nil, nil
	}
	if t.Elem != nil {
		list, ok := v.([]any)
		if !ok {
			item, err := c.input(t.Elem, v, join(path, 0))
			if err != nil {
				return nil, err
			}
			return []any{item}, nil
		}
		out := make([]any, 0, len(list))
		for i, item := range list {
			cv, err := c.input(t.Elem, item, join(path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, cv)
		}
		return out, nil
	}
	def := c.schema.Types[t.NamedType]
	if def == nil {
		return nil, fmt.Errorf("unknown type %s", t.NamedType)
	}
	switch def.Kind {
	case ast.Scalar:
		return scalarInput(def.Name, v)
	case ast.Enum:
		s, ok := v.(string)
		if !ok || def.EnumValues.ForName(s) == nil {
			return nil, fmt.Errorf("not a value of enum %s", def.Name)
		}
		return s, nil
	case ast.InputObject:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("not an input object %s", def.Name)
		}
		for name := range obj {
			if def.Fields.ForName(name) == nil {
				return nil, fmt.Errorf("unknown input field %s.%s", def.Name, name)
			}
		}
		if isOneOf(def) && !oneEntryNotNull(obj) {
			return nil, fmt.Errorf("OneOf Input Object %s needs exactly one non-null field", def.Name)
		}
		return c.inputObject(def, func(name string) (any, bool, error) {
			fv, has := obj[name]
			if !has {
				return nil, false, nil
			}
			cv, err := c.input(def.Fields.ForName(name).Type, fv, join(path, name))
			return cv, err == nil, err
		}, path)
	}
	return nil, fmt.Errorf("%s is not an input type", def.Name)
}

func oneEntryNotNull(obj map[string]any) bool {
	if len(obj) != 1 {
		return false
	}
	for _, v := range obj {
		return v != nil
	}
	return false
}

// scalarInput applies the input coercion of the built-in scalars to a JSON
// value. Int is 32-bit and accepts only integral numbers, never a string.
func scalarInput(name string, v any) (any, error) {
	switch name {
	case "Int":
		if n, ok := v.(json.Number); ok {
			if i, ok := integral(n, math.MinInt32, math.MaxInt32); ok {
				return json.Number(strconv.FormatInt(i, 10)), nil
			}
		}
	case "Float":
		if n, ok := v.(json.Number); ok {
			if f, err := n.Float64(); err == nil && !math.IsInf(f, 0) {
				return n, nil
			}
		}
	case "String":
		if s, ok := v.(string); ok {
			return s, nil
		}
	case "Boolean":
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case "ID":
		switch id := v.(type) {
		case string:
			return id, nil
		case json.Number:
			if i, ok := integral(id, math.MinInt64, math.MaxInt64); ok {
				return strconv.FormatInt(i, 10), nil
			}
		}
	default:
		return v, nil
	}
	return nil, fmt.Errorf("not a valid %s", name)
}

// integral reads a JSON number whose exact value is an integer within
// [lo, hi]. It works on the decimal text, never through a float64: 2024,
// 2024.0 and 2.024e3 are the same Int, while 2024.0000000000000001 is not
// an integer although a float64 rounds it to one.
func integral(n json.Number, lo, hi int64) (int64, bool) {
	s := string(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(s), "e")
	intPart, frac, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(intPart+frac, "0")
	if digits == "" {
		return 0, lo <= 0 && 0 <= hi
	}
	exp := 0
	if hasExponent {
		e, err := strconv.Atoi(exponent)
		// Beyond ±2^20 a non-zero value is either far outside int64 or
		// keeps a fraction (the body holds fewer digits than that). The
		// bound also keeps the arithmetic below from overflowing.
		if err != nil || e > 1<<20 || e < -(1<<20) {
			return 0, false
		}
		exp = e
	}
	// The value is digits × 10^exp, with no trailing zero in digits.
	exp -= len(frac)
	trimmed := strings.TrimRight(digits, "0")
	exp += len(digits) - len(trimmed)
	if exp < 0 || exp > 19-len(trimmed) {
		return 0, false // a fraction remains, or beyond int64
	}
	text := trimmed + strings.Repeat("0", exp)
	if neg {
		text = "-" + text
	}
	i, err := strconv.ParseInt(text, 10, 64)
	if err != nil || i < lo || i > hi {
		return 0, false
	}
	return i, true
}

// join returns a new path: path followed by segs.
func join(path []any, segs ...any) []any {
	out := make([]any, 0, len(path)+len(segs))
	return append(append(out, path...), segs...)
}
