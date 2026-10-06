// Package ftvgraphql is the mapping function of the FTV GraphQL profile for
// the NLgov AuthZEN Authorization API (draft-01, Section 6).
//
// A PEP sends one AuthZEN evaluation per GraphQL request and never parses
// the body. Map turns that request into the field list a policy engine binds
// rules to: every selected field, its parent type from the bundled schema,
// and its arguments as the source will execute them. Whenever the mapper
// cannot be certain the list is complete and correct, it returns no fields
// and says why in Unverifiable, so the engine denies.
//
// The package knows no PDP, policy engine or transport. The PDP adapter
// fills a Request from the AuthZEN request and places the Output at
// resource.properties.graphql.
package ftvgraphql

import (
	"fmt"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Profile identifies the representation of Output.
const Profile = "ftv-graphql/0.1"

// Codes and subcodes of Section 6.8.
const (
	CodeCoverageUnverifiable  = "COVERAGE_UNVERIFIABLE"
	CodeOperationNotSupported = "OPERATION_NOT_SUPPORTED"
	CodeConfigError           = "CONFIG_ERROR"

	SubUnsupportedTransport = "UNSUPPORTED_TRANSPORT"
	SubInvalidBody          = "INVALID_BODY"
	SubParseError           = "PARSE_ERROR"
	SubInvalidQuery         = "INVALID_QUERY"
	SubNoOperation          = "NO_OPERATION"
	SubOperationNotFound    = "OPERATION_NOT_FOUND"
	SubOperationAmbiguous   = "OPERATION_AMBIGUOUS"
	SubVariableError        = "VARIABLE_ERROR"
	SubLimitExceeded        = "LIMIT_EXCEEDED"
	SubSchemaUnavailable    = "SCHEMA_UNAVAILABLE"
)

// Request is the part of the AuthZEN request the mapper reads (Section 5.2).
type Request struct {
	Method  string         // action.name
	Target  string         // resource.id: path and query string as received
	Headers map[string]any // context.headers
	Body    any            // action.properties.body; nil when absent
}

// Settings are the bundle settings the mapper applies.
type Settings struct {
	// GraphQLPath is the one path the source serves GraphQL on. Compared
	// byte for byte with Request.Target.
	GraphQLPath string
	// AllowedExtensions lists the members the body's extensions object may
	// carry. persistedQuery is never allowed, listed or not.
	AllowedExtensions []string
	Limits            Limits
}

// Limits of Section 6.7. A zero value takes the default.
type Limits struct {
	BodyBytes           int
	NestingDepth        int
	FragmentDefinitions int
	ValidationTime      time.Duration
	SelectionDepth      int
	FieldRecords        int
	AliasedSelections   int
}

// Output is the object the mapper adds at resource.properties.graphql
// (Section 6.2).
type Output struct {
	Profile      string        `json:"profile"`
	Operation    *Operation    `json:"operation"`
	Schema       *SchemaRef    `json:"schema"`
	Fields       []Field       `json:"fields"`
	Unverifiable *Unverifiable `json:"unverifiable"`
}

// Operation is the selected operation. Nil in Output until one is selected.
type Operation struct {
	Type string  `json:"type"`
	Name *string `json:"name"`
}

// SchemaRef identifies the bundled SDL the request was mapped against.
type SchemaRef struct {
	Digest string `json:"digest"`
}

// Field is one selection, after fragment expansion (Section 6.2).
type Field struct {
	Path       []string       `json:"path"`
	Alias      string         `json:"alias,omitempty"`
	ParentType string         `json:"parentType"`
	On         string         `json:"on,omitempty"`
	Field      string         `json:"field"`
	Leaf       bool           `json:"leaf"`
	Args       map[string]Arg `json:"args,omitempty"`
}

// Arg is one argument of a selection, as the source executes it (Section 6.5).
type Arg struct {
	Value          any      `json:"value"`
	Origin         string   `json:"origin"`
	Variables      []string `json:"variables,omitempty"`
	SchemaDefaults [][]any  `json:"schemaDefaults,omitempty"`
}

// Unverifiable says why the field list is absent (Section 6.8).
type Unverifiable struct {
	Code    string `json:"code"`
	Subcode string `json:"subcode,omitempty"`
	Message string `json:"message"`
}

func (u *Unverifiable) Error() string {
	return fmt.Sprintf("%s/%s: %s", u.Code, u.Subcode, u.Message)
}

func unverifiable(subcode, format string, args ...any) *Unverifiable {
	return &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: subcode, Message: fmt.Sprintf(format, args...)}
}

// Map maps one request. schema is nil when no SDL could be loaded; the
// request then fails closed with SCHEMA_UNAVAILABLE once it gets as far as
// validation.
func Map(req Request, schema *Schema, settings Settings) Output {
	out := Output{Profile: Profile, Fields: []Field{}}
	if schema != nil {
		out.Schema = &SchemaRef{Digest: schema.Digest}
	}
	m := mapping{schema: schema, settings: settings, limits: settings.Limits.withDefaults()}
	fields, fail := m.run(req)
	out.Operation = m.operation
	if fail != nil {
		out.Unverifiable = fail
		return out
	}
	out.Fields = fields
	return out
}

type mapping struct {
	schema    *Schema
	settings  Settings
	limits    Limits
	operation *Operation
	// typeSystemAt is the first type-system definition in the document,
	// which fails validation (Executable Definitions).
	typeSystemAt *ast.Position
	aliases      map[position]bool
}

// run applies the checks in the order Section 6.8 fixes: transport, body
// size, body shape, parse, no operation, validation, operation selection,
// variables, walk limits.
func (m *mapping) run(req Request) ([]Field, *Unverifiable) {
	body, fail := checkTransport(req, m.settings.GraphQLPath)
	if fail != nil {
		return nil, fail
	}
	if len(body) > m.limits.BodyBytes {
		return nil, unverifiable(SubLimitExceeded, "body size %d over %d bytes", len(body), m.limits.BodyBytes)
	}
	gql, fail := decodeBody(body, m.limits.NestingDepth, m.settings.AllowedExtensions)
	if fail != nil {
		return nil, fail
	}
	doc, fail := m.parse(gql.Query)
	if fail != nil {
		return nil, fail
	}
	if m.schema == nil {
		return nil, &Unverifiable{Code: CodeConfigError, Subcode: SubSchemaUnavailable, Message: "no GraphQL schema loaded"}
	}
	if fail := validate(m.schema.schema, doc, m.typeSystemAt, m.limits.ValidationTime); fail != nil {
		return nil, fail
	}
	op, fail := selectOperation(doc, gql.OperationName)
	if fail != nil {
		return nil, fail
	}
	m.operation = &Operation{Type: string(op.Operation)}
	if op.Name != "" {
		m.operation.Name = &op.Name
	}
	if op.Operation != ast.Query {
		return nil, &Unverifiable{Code: CodeOperationNotSupported, Message: fmt.Sprintf("operation type %s", op.Operation)}
	}
	vars, fail := coerceVariables(m.schema.schema, op, gql.Variables)
	if fail != nil {
		return nil, fail
	}
	if fail := checkArguments(m.schema.schema, doc, op, vars); fail != nil {
		return nil, fail
	}
	return walk(m.schema.schema, doc, op, vars, m.aliases, m.limits)
}

// parse parses the document and applies the limits of the parse stage.
func (m *mapping) parse(query string) (*ast.QueryDocument, *Unverifiable) {
	if depth := queryNestingDepth(query); depth > m.limits.NestingDepth {
		return nil, unverifiable(SubLimitExceeded, "GraphQL nesting depth over %d", m.limits.NestingDepth)
	}
	scan, err := scanDocument(query)
	if err != nil {
		return nil, unverifiable(SubParseError, "%s", err.Error())
	}
	if scan.typeSystem != "" {
		if _, err := parser.ParseSchema(&ast.Source{Name: "query", Input: scan.typeSystem}); err != nil {
			return nil, unverifiable(SubParseError, "%s", err.Error())
		}
	}
	m.typeSystemAt, m.aliases = scan.typeSystemAt, scan.aliases
	doc, err := parser.ParseQuery(&ast.Source{Name: "query", Input: scan.executable})
	if err != nil {
		return nil, unverifiable(SubParseError, "%s", err.Error())
	}
	if len(doc.Fragments) > m.limits.FragmentDefinitions {
		return nil, unverifiable(SubLimitExceeded, "%d fragment definitions over %d", len(doc.Fragments), m.limits.FragmentDefinitions)
	}
	// Before validation: a fragment-only document also fails Fragments Must
	// Be Used, and this code is the useful one (Section 6.3, step 2).
	if len(doc.Operations) == 0 {
		return nil, unverifiable(SubNoOperation, "document has no operation")
	}
	return doc, nil
}

// selectOperation follows GetOperation of the GraphQL specification
// (Section 6.3, steps 4 and 5).
func selectOperation(doc *ast.QueryDocument, name *string) (*ast.OperationDefinition, *Unverifiable) {
	if name != nil {
		op := doc.Operations.ForName(*name)
		if op == nil {
			return nil, unverifiable(SubOperationNotFound, "no operation named by operationName")
		}
		return op, nil
	}
	if len(doc.Operations) > 1 {
		return nil, unverifiable(SubOperationAmbiguous, "%d operations and no operationName", len(doc.Operations))
	}
	return doc.Operations[0], nil
}

func (l Limits) withDefaults() Limits {
	def := func(v, d int) int {
		if v > 0 {
			return v
		}
		return d
	}
	if l.ValidationTime <= 0 {
		l.ValidationTime = time.Second
	}
	l.BodyBytes = def(l.BodyBytes, 1<<20)
	l.NestingDepth = def(l.NestingDepth, 64)
	l.FragmentDefinitions = def(l.FragmentDefinitions, 100)
	l.SelectionDepth = def(l.SelectionDepth, 64)
	l.FieldRecords = def(l.FieldRecords, 1000)
	l.AliasedSelections = def(l.AliasedSelections, 100)
	return l
}
