// Command check verifies the conformance matrix of the ASI route against the
// list of requirement IDs of ETSI TS 119 478 (slice 6 of #336):
//
//   - every ID in the list has an entry or falls under a group;
//   - every entry and group names an ID or prefix of the list;
//   - every status is one of passed, stub, not-applicable or failing, with
//     the fields that status requires;
//   - every test an entry names exists as a Go test function.
//
// The tests themselves run in the CI jobs of the services. On success it
// prints a summary per role.
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
