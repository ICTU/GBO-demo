package ftvgraphql

import (
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/lexer"
)

// scannedDocument is a GraphQL document split for gqlparser, whose query
// parser reads neither of two things the GraphQL specification (September
// 2025) allows in a document:
//
//   - descriptions on operations, fragments and variable definitions;
//   - type-system definitions and extensions. A document that holds one
//     parses, and then fails validation (Executable Definitions).
//
// Both are blanked rune by rune, newlines kept, so every position gqlparser
// reports is the position in the original document.
type scannedDocument struct {
	executable string // descriptions and type-system definitions blanked
	typeSystem string // only the type-system definitions; "" when none
	// typeSystemAt is the start of the first type-system definition, nil
	// when there is none.
	typeSystemAt *ast.Position
	// aliases holds the position of every field written with an alias.
	// gqlparser sets Field.Alias to the name when none is written, so
	// `naam: naam` cannot be told from `naam` without it.
	aliases map[position]bool
}

// position is a line and column in the document.
type position struct{ line, column int }

// scanDocument follows the Document grammar far enough to find where each
// definition starts and ends. What lies inside a selection set, an argument
// list or a fields definition is left to the parsers.
func scanDocument(query string) (scannedDocument, error) {
	toks, err := tokens(query)
	if err != nil {
		return scannedDocument{}, err
	}
	s := &docScanner{toks: toks, aliases: map[position]bool{}}
	if err := s.document(); err != nil {
		return scannedDocument{}, err
	}
	runes := []rune(query)
	out := scannedDocument{executable: blank(runes, s.descriptions, s.typeSystem, true), aliases: s.aliases}
	if len(s.typeSystem) > 0 {
		out.typeSystem = blank(runes, s.typeSystem, nil, false)
		out.typeSystemAt = s.typeSystemAt
	}
	return out, nil
}

func tokens(query string) ([]lexer.Token, error) {
	lex := lexer.New(&ast.Source{Name: "query", Input: query})
	var toks []lexer.Token
	for {
		tok, err := lex.ReadToken()
		if err != nil {
			return nil, err
		}
		if tok.Kind == lexer.Comment {
			continue
		}
		toks = append(toks, tok)
		if tok.Kind == lexer.EOF {
			return toks, nil
		}
	}
}

// span is a rune range [start, end) of the document.
type span struct{ start, end int }

// blank returns runes with the given spans replaced by spaces (inside) or
// everything but the spans replaced (inside false). Newlines stay, so lines
// and columns do not move.
func blank(runes []rune, a, b []span, inside bool) string {
	in := make([]bool, len(runes))
	for _, sp := range append(append([]span(nil), a...), b...) {
		for i := sp.start; i < sp.end && i < len(in); i++ {
			in[i] = true
		}
	}
	out := make([]rune, len(runes))
	for i, r := range runes {
		if in[i] == inside && r != '\n' && r != '\r' {
			r = ' '
		}
		out[i] = r
	}
	return string(out)
}

type docScanner struct {
	toks         []lexer.Token
	i            int
	aliases      map[position]bool
	descriptions []span
	typeSystem   []span
	typeSystemAt *ast.Position
}

var typeSystemKeywords = map[string]bool{
	"schema": true, "scalar": true, "type": true, "interface": true,
	"union": true, "enum": true, "input": true, "directive": true, "extend": true,
}

func (s *docScanner) peek() lexer.Token { return s.toks[s.i] }

func (s *docScanner) next() lexer.Token {
	t := s.toks[s.i]
	if t.Kind != lexer.EOF {
		s.i++
	}
	return t
}

func (s *docScanner) at(kind lexer.Type, value ...string) bool {
	t := s.peek()
	return t.Kind == kind && (len(value) == 0 || t.Value == value[0])
}

func (s *docScanner) skip(kind lexer.Type, value ...string) bool {
	if s.at(kind, value...) {
		s.next()
		return true
	}
	return false
}

func (s *docScanner) expect(kind lexer.Type, value ...string) error {
	if s.skip(kind, value...) {
		return nil
	}
	return s.unexpected()
}

func (s *docScanner) unexpected() error {
	t := s.peek()
	return fmt.Errorf("query:%d:%d: Unexpected %s", t.Pos.Line, t.Pos.Column, t.String())
}

// tokenStart is where a token begins in the document. gqlparser reports
// the column of a string after its opening quotes; its span includes them.
func tokenStart(t lexer.Token) *ast.Position {
	pos := t.Pos
	switch t.Kind {
	case lexer.String:
		pos.Column--
	case lexer.BlockString:
		pos.Column -= 3
	}
	return &pos
}

func (s *docScanner) isDescription() bool {
	return s.at(lexer.String) || s.at(lexer.BlockString)
}

func (s *docScanner) document() error {
	for !s.at(lexer.EOF) {
		start := s.peek()
		var desc *lexer.Token
		if s.isDescription() {
			t := s.next()
			desc = &t
		}
		t := s.peek()
		var err error
		switch {
		case t.Kind == lexer.BraceL && desc == nil:
			err = s.selectionSet()
		case t.Kind == lexer.Name && (t.Value == "query" || t.Value == "mutation" || t.Value == "subscription"):
			err = s.operation()
		case t.Kind == lexer.Name && t.Value == "fragment":
			err = s.fragment()
		case t.Kind == lexer.Name && typeSystemKeywords[t.Value]:
			if err = s.typeSystemDefinition(); err == nil {
				s.typeSystem = append(s.typeSystem, span{start.Pos.Start, s.toks[s.i-1].Pos.End})
				if s.typeSystemAt == nil {
					s.typeSystemAt = tokenStart(start)
				}
			}
			continue
		default:
			// A description on the query shorthand is not allowed.
			return s.unexpected()
		}
		if err != nil {
			return err
		}
		if desc != nil {
			s.descriptions = append(s.descriptions, span{desc.Pos.Start, desc.Pos.End})
		}
	}
	return nil
}

// operation: OperationType Name? VariablesDefinition? Directives? SelectionSet
func (s *docScanner) operation() error {
	s.next()
	s.skip(lexer.Name)
	if s.at(lexer.ParenL) {
		if err := s.variableDefinitions(); err != nil {
			return err
		}
	}
	if err := s.directives(); err != nil {
		return err
	}
	return s.selectionSet()
}

// fragment: fragment FragmentName on NamedType Directives? SelectionSet
func (s *docScanner) fragment() error {
	s.next()
	if err := s.expect(lexer.Name); err != nil {
		return err
	}
	if err := s.expect(lexer.Name, "on"); err != nil {
		return err
	}
	if err := s.expect(lexer.Name); err != nil {
		return err
	}
	if err := s.directives(); err != nil {
		return err
	}
	return s.selectionSet()
}

// variableDefinitions: ( (Description? $Name : Type DefaultValue? Directives?)+ )
func (s *docScanner) variableDefinitions() error {
	s.next()
	for !s.skip(lexer.ParenR) {
		if s.isDescription() {
			d := s.next()
			s.descriptions = append(s.descriptions, span{d.Pos.Start, d.Pos.End})
		}
		for _, kind := range []lexer.Type{lexer.Dollar, lexer.Name, lexer.Colon} {
			if err := s.expect(kind); err != nil {
				return err
			}
		}
		if err := s.typeRef(); err != nil {
			return err
		}
		if s.skip(lexer.Equals) {
			if err := s.value(); err != nil {
				return err
			}
		}
		if err := s.directives(); err != nil {
			return err
		}
	}
	return nil
}

func (s *docScanner) typeRef() error {
	if s.skip(lexer.BracketL) {
		if err := s.typeRef(); err != nil {
			return err
		}
		if err := s.expect(lexer.BracketR); err != nil {
			return err
		}
	} else if err := s.expect(lexer.Name); err != nil {
		return err
	}
	s.skip(lexer.Bang)
	return nil
}

func (s *docScanner) value() error {
	switch s.peek().Kind {
	case lexer.BracketL, lexer.BraceL:
		return s.group()
	case lexer.Dollar:
		s.next()
		return s.expect(lexer.Name)
	case lexer.Name, lexer.Int, lexer.Float, lexer.String, lexer.BlockString:
		s.next()
		return nil
	}
	return s.unexpected()
}

func (s *docScanner) directives() error {
	for s.skip(lexer.At) {
		if err := s.expect(lexer.Name); err != nil {
			return err
		}
		if s.at(lexer.ParenL) {
			if err := s.group(); err != nil {
				return err
			}
		}
	}
	return nil
}

// selectionSet skips a selection set and records its aliases. Outside
// parentheses (arguments, directive arguments, the values inside them) a
// selection set holds no `Name :` but an alias.
func (s *docScanner) selectionSet() error {
	if !s.at(lexer.BraceL) {
		return s.unexpected()
	}
	depth, parens := 0, 0
	for {
		t := s.next()
		switch t.Kind {
		case lexer.BraceL, lexer.BracketL:
			depth++
		case lexer.BraceR, lexer.BracketR:
			depth--
		case lexer.ParenL:
			depth++
			parens++
		case lexer.ParenR:
			depth--
			parens--
		case lexer.Name:
			if parens == 0 && s.at(lexer.Colon) {
				s.aliases[position{t.Pos.Line, t.Pos.Column}] = true
			}
		case lexer.EOF:
			return s.unexpected()
		}
		if depth == 0 {
			return nil
		}
	}
}

// group skips a balanced (…), […] or {…} group, starting at its opener.
func (s *docScanner) group() error {
	depth := 0
	for {
		t := s.next()
		switch t.Kind {
		case lexer.ParenL, lexer.BracketL, lexer.BraceL:
			depth++
		case lexer.ParenR, lexer.BracketR, lexer.BraceR:
			depth--
		case lexer.EOF:
			return s.unexpected()
		}
		if depth == 0 {
			return nil
		}
		if depth < 0 {
			return fmt.Errorf("query:%d:%d: Unexpected %s", t.Pos.Line, t.Pos.Column, t.String())
		}
	}
}

// typeSystemDefinition skips one type-system definition or extension. Its
// syntax is checked afterwards by the schema parser.
func (s *docScanner) typeSystemDefinition() error {
	keyword := s.next().Value
	if keyword == "extend" {
		t := s.peek()
		if t.Kind != lexer.Name || !typeSystemKeywords[t.Value] || t.Value == "directive" || t.Value == "extend" {
			return s.unexpected()
		}
		keyword = s.next().Value
	}
	switch keyword {
	case "schema":
		return s.optionalBody()
	case "scalar":
		if err := s.expect(lexer.Name); err != nil {
			return err
		}
		return s.directives()
	case "type", "interface":
		if err := s.expect(lexer.Name); err != nil {
			return err
		}
		if s.skip(lexer.Name, "implements") {
			if err := s.nameList(lexer.Amp); err != nil {
				return err
			}
		}
		return s.optionalBody()
	case "union":
		if err := s.expect(lexer.Name); err != nil {
			return err
		}
		if err := s.directives(); err != nil {
			return err
		}
		if s.skip(lexer.Equals) {
			return s.nameList(lexer.Pipe)
		}
		return nil
	case "enum", "input":
		if err := s.expect(lexer.Name); err != nil {
			return err
		}
		return s.optionalBody()
	default: // directive
		if err := s.expect(lexer.At); err != nil {
			return err
		}
		if err := s.expect(lexer.Name); err != nil {
			return err
		}
		if s.at(lexer.ParenL) {
			if err := s.group(); err != nil {
				return err
			}
		}
		s.skip(lexer.Name, "repeatable")
		if err := s.expect(lexer.Name, "on"); err != nil {
			return err
		}
		return s.nameList(lexer.Pipe)
	}
}

// optionalBody: Directives? { … }?
func (s *docScanner) optionalBody() error {
	if err := s.directives(); err != nil {
		return err
	}
	if s.at(lexer.BraceL) {
		return s.group()
	}
	return nil
}

// nameList: sep? Name (sep Name)*
func (s *docScanner) nameList(sep lexer.Type) error {
	s.skip(sep)
	if err := s.expect(lexer.Name); err != nil {
		return err
	}
	for s.skip(sep) {
		if err := s.expect(lexer.Name); err != nil {
			return err
		}
	}
	return nil
}
