// Command check verifies a conformance matrix against the list of
// requirement IDs of its specification: ETSI TS 119 478 for the ASI route
// (slice 6 of #336), and the specifications of the own credential issuer
// (#614):
//
//   - every ID in the list has an entry or falls under a group;
//   - every entry and group names an ID, prefix or role of the list;
//   - every status is one of passed, stub, not-applicable, failing or
//     deviation, with the fields that status requires, also per profile;
//   - every test an entry names exists as a Go test function.
//
// The tests themselves run in the CI jobs of the services. On success it
// prints a summary per role, and per profile when the matrix has profiles.
//
//	go run . -matrix ../etsi-ts-119478.yaml -root ../..
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	matrix := flag.String("matrix", "../etsi-ts-119478.yaml", "the conformance matrix")
	root := flag.String("root", "../..", "the repository root the test references are relative to")
	flag.Parse()
	os.Exit(run(*matrix, *root, os.Stdout, os.Stderr))
}

func run(matrixPath, root string, stdout, stderr io.Writer) int {
	m, err := Load(matrixPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report, problems := Check(m, root)
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stderr, p)
		}
		fmt.Fprintf(stderr, "%d problem(s) in %s\n", len(problems), matrixPath)
		return 1
	}
	fmt.Fprint(stdout, report)
	return 0
}
