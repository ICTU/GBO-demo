// Command extract lists the normative statements of an OpenID specification
// (the HTML that xml2rfc renders) as requirement IDs, for a conformance
// matrix (#614). A statement is a sentence with an RFC 2119 keyword of
// obligation or recommendation (MUST, SHALL, SHOULD, REQUIRED, RECOMMENDED,
// and their negations; not MAY or OPTIONAL). Its ID is
// <prefix>-<section>-<nn>: the section it is in and its order there. The
// sections on notation and terminology, the references and the document
// history are skipped.
//
// The output is the ID list, identifiers only. With -roles, the roles of an
// earlier list are kept, so the list can be made again. With -text, each
// statement is also written there, for review: the text is not committed.
//
//	go run ./extract -spec spec.html -prefix HAIP -roles ../haip-ids.txt > ../haip-ids.txt
package main

import (
	"bufio"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"strings"
)

func main() {
	spec := flag.String("spec", "", "the specification, as HTML")
	prefix := flag.String("prefix", "", "the ID prefix, for example HAIP")
	roles := flag.String("roles", "", "an earlier ID list whose roles to keep")
	text := flag.String("text", "", "a file to write the statements to, for review")
	title := flag.String("title", "", "the specification's name for the header")
	flag.Parse()
	if *spec == "" || *prefix == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*spec, *prefix, *roles, *text, *title, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(specPath, prefix, rolesPath, textPath, title string, out io.Writer) error {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	known := map[string]string{}
	if rolesPath != "" {
		if known, err = readRoles(rolesPath); err != nil {
			return err
		}
	}
	statements := Extract(string(raw), prefix)
	if title == "" {
		title = prefix
	}
	fmt.Fprintf(out, "# The normative statements of %s, in document order: identifiers\n# and roles only, no text. Made with conformance/check/extract.\n", title)
	var review strings.Builder
	for _, s := range statements {
		if role := known[s.ID]; role != "" {
			fmt.Fprintf(out, "%s %s\n", s.ID, role)
		} else {
			fmt.Fprintln(out, s.ID)
		}
		fmt.Fprintf(&review, "%s\t%s\n", s.ID, s.Text)
	}
	if textPath != "" {
		return os.WriteFile(textPath, []byte(review.String()), 0o644)
	}
	return nil
}

func readRoles(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	roles := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if fields := strings.Fields(s.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			roles[fields[0]] = fields[1]
		}
	}
	return roles, s.Err()
}

// Statement is a normative sentence and its ID.
type Statement struct {
	ID, Section, Text string
}

var (
	// block is a paragraph, list item or definition with an xml2rfc ID;
	// its own text ends at its pilcrow, before any nested list.
	block = regexp.MustCompile(`(?s)<(p|li|dd|dt|td)\b[^>]*\bid="((?:section|appendix)-([^"-]+)-[^"]+)"[^>]*>(.*?)<a href="#([^"]+)" class="pilcrow">`)
	// heading is a section's number and name.
	heading = regexp.MustCompile(`(?s)<section id="(?:section|appendix)-([^"]+)">\s*<h\d[^>]*>.*?class="section-name selfRef">(.*?)</a>`)
	tag     = regexp.MustCompile(`(?s)<[^>]+>`)
	space   = regexp.MustCompile(`\s+`)
	keyword = regexp.MustCompile(`\b(MUST|SHALL|SHOULD|REQUIRED|RECOMMENDED)\b`)
	// skipped are the sections without requirements on an implementation.
	skipped = regexp.MustCompile(`(?i)notation|conventions|terminology|references|document history|acknowledg|notices|IANA`)
)

// Extract returns the normative statements of an xml2rfc HTML document.
func Extract(doc, prefix string) []Statement {
	names := map[string]string{}
	for _, m := range heading.FindAllStringSubmatch(doc, -1) {
		names[m[1]] = clean(m[2])
	}
	var out []Statement
	count := map[string]int{}
	for _, m := range block.FindAllStringSubmatch(doc, -1) {
		if m[2] != m[5] {
			continue
		}
		section := m[3]
		if section == "abstract" || strings.HasPrefix(section, "toc") || skippedSection(section, names) {
			continue
		}
		for _, sentence := range sentences(clean(m[4])) {
			if !keyword.MatchString(sentence) {
				continue
			}
			count[section]++
			out = append(out, Statement{ID: fmt.Sprintf("%s-%s-%02d", prefix, section, count[section]), Section: section, Text: sentence})
		}
	}
	return out
}

// skippedSection tells whether section, or a section it is part of, is one
// without requirements.
func skippedSection(section string, names map[string]string) bool {
	for s := section; s != ""; {
		if skipped.MatchString(names[s]) {
			return true
		}
		i := strings.LastIndex(s, ".")
		if i < 0 {
			break
		}
		s = s[:i]
	}
	return false
}

func clean(s string) string {
	s = tag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "¶", "")
	return strings.TrimSpace(space.ReplaceAllString(s, " "))
}

// sentences splits text after ".", "!" or "?" followed by a space and a
// capital letter, so that "e.g. x" and "Section 4.1 of" stay whole.
func sentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i+2 < len(text); i++ {
		c := text[i]
		if (c == '.' || c == '!' || c == '?') && text[i+1] == ' ' && text[i+2] >= 'A' && text[i+2] <= 'Z' && !abbreviation(text[:i+1]) {
			out = append(out, strings.TrimSpace(text[start:i+1]))
			start = i + 2
		}
	}
	return append(out, strings.TrimSpace(text[start:]))
}

func abbreviation(s string) bool {
	for _, a := range []string{"e.g.", "i.e.", "etc.", "cf.", "vs."} {
		if strings.HasSuffix(s, a) {
			return true
		}
	}
	return false
}
