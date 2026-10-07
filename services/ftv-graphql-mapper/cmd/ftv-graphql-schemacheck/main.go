// Command ftv-graphql-schemacheck checks guarantee H3 of the FTV GraphQL
// profile for one service: every element of the schema the PDP validates
// against has the same definition in the source's schema (Sections 7.4 and
// 11). The source's schema comes from a running source, by introspection,
// or from a saved introspection response, e.g. of a build not yet deployed.
//
//	ftv-graphql-schemacheck -catalog <dir> -service <name> -url <graphql-url>
//	ftv-graphql-schemacheck -catalog <dir> -service <name> -introspection <file>
//
// Exit status 0: no drift. 1: drift, listed on stderr. 2: the check could
// not be made, which vouches for nothing either.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	ftvgraphql "gbo-demo/ftv-graphql-mapper"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ftv-graphql-schemacheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	catalogDir := flags.String("catalog", "", "directory with services.json and the bundled schemas")
	service := flags.String("service", "", "FSC service name, as in services.json")
	url := flags.String("url", "", "the source's GraphQL endpoint, asked by introspection")
	file := flags.String("introspection", "", "a saved introspection response")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *catalogDir == "" || *service == "" || (*url == "") == (*file == "") {
		fmt.Fprintln(stderr, "usage: ftv-graphql-schemacheck -catalog <dir> -service <name> (-url <graphql-url> | -introspection <file>)")
		return 2
	}

	catalog, err := ftvgraphql.LoadCatalog(os.DirFS(*catalogDir))
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	svc, ok := catalog.Service(*service)
	if !ok || svc.Err != nil {
		fmt.Fprintf(stderr, "error: service %s: not in the catalog or its schema does not load: %v\n", *service, svc.Err)
		return 2
	}

	var response []byte
	if *url != "" {
		response, err = introspect(*url)
	} else {
		response, err = os.ReadFile(*file)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	source, err := ftvgraphql.ParseIntrospection(response)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	drift := svc.Schema.Drift(source)
	if len(drift) == 0 {
		fmt.Fprintf(stdout, "%s: no drift from the bundled schema\n", *service)
		return 0
	}
	fmt.Fprintf(stderr, "%s: the source differs from the bundled schema (H3):\n", *service)
	for _, d := range drift {
		fmt.Fprintln(stderr, "  "+d)
	}
	return 1
}

// introspect asks a source for its schema, in the transport the profile
// allows: POST with a JSON body.
func introspect(url string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{"query": ftvgraphql.IntrospectionQuery})
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("introspection: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("introspection: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspection: status %d", resp.StatusCode)
	}
	return data, nil
}
