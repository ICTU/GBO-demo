// Package cli runs the reference QTSP's flow from the command line, against
// any ASIP reachable through a Discover Interface: discover, register,
// authorize (the chooser played by the test harness), token, and verify or
// retrieve with the seal checked. Options marked "test harness" provoke the
// error answers of the acceptance criteria; a real QTSP has none of them.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gbo-demo/qtsp-reference/internal/harness"
	"gbo-demo/qtsp-reference/internal/qtsp"
)

// Options of one run.
type Options struct {
	// Operation is verify or retrieve.
	Operation  string
	Person     string
	Attributes []qtsp.Attribute
	// Test harness: the user denies; the token is tampered with (401); the
	// request claims a mandate (501); the body is malformed (400); an
	// attribute that was not discovered is added to the body (404).
	Deny, TamperToken, Mandate, Malformed bool
	ExtraAttribute                        string
}

// Outcome is what a run brought.
type Outcome struct {
	Plan     qtsp.Plan
	Denied   *harness.Callback
	Response qtsp.Response
}

// Parse reads the options of a verify or retrieve command. For verify each
// --attr is identifier=JSON value; for retrieve an identifier.
func Parse(operation string, args []string, stderr io.Writer) (Options, error) {
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := Options{Operation: operation}
	var attrs multi
	fs.Var(&attrs, "attr", "attribute: identifier=JSON value (verify) or identifier (retrieve); repeatable")
	fs.StringVar(&o.Person, "person", "frouke", "test person the harness picks at the chooser")
	fs.BoolVar(&o.Deny, "deny", false, "test harness: the user denies the request")
	fs.BoolVar(&o.TamperToken, "tamper-token", false, "test harness: send a tampered access token (401)")
	fs.BoolVar(&o.Mandate, "mandate", false, "test harness: claim to act for another person (501)")
	fs.BoolVar(&o.Malformed, "malformed", false, "test harness: send a malformed body (400)")
	fs.StringVar(&o.ExtraAttribute, "extra-attribute", "", "test harness: add an attribute that was not discovered (404)")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if len(attrs) == 0 {
		return Options{}, errors.New("at least one --attr is required")
	}
	for _, a := range attrs {
		id, value, hasValue := strings.Cut(a, "=")
		switch {
		case operation == "verify" && (!hasValue || !json.Valid([]byte(value))):
			return Options{}, fmt.Errorf("--attr %q: verify needs identifier=JSON value", a)
		case operation == "retrieve" && hasValue:
			return Options{}, fmt.Errorf("--attr %q: retrieve needs only the identifier", a)
		}
		o.Attributes = append(o.Attributes, qtsp.Attribute{Identifier: id, Value: json.RawMessage(value)})
	}
	return o, nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Run runs the flow once. httpClient is the transport of c, for the harness.
func Run(ctx context.Context, c *qtsp.Client, httpClient *http.Client, o Options) (Outcome, error) {
	ids := make([]string, 0, len(o.Attributes))
	for _, a := range o.Attributes {
		ids = append(ids, a.Identifier)
	}
	plan, err := c.Plan(ctx, ids)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Plan: plan}
	reg, err := Register(ctx, c, plan.Server, 30*time.Second)
	if err != nil {
		return out, err
	}
	verifier, challenge := qtsp.PKCE()
	state := qtsp.State()
	cb, err := harness.Decide(ctx, httpClient, c.AuthorizationURL(plan, reg, o.Operation, state, challenge), o.Person, !o.Deny)
	if err != nil {
		return out, err
	}
	if cb.Error != "" {
		out.Denied = &cb
		return out, nil
	}
	token, err := c.Token(ctx, plan, reg, cb.Code, verifier)
	if err != nil {
		return out, err
	}
	if o.TamperToken {
		token = tamper(token)
	}
	body := body(o)
	out.Response, err = c.Call(ctx, plan, o.Operation, token, body)
	return out, err
}

func body(o Options) []byte {
	if o.Malformed {
		return []byte(`{}`)
	}
	attrs := o.Attributes
	if o.ExtraAttribute != "" {
		attrs = append(attrs, qtsp.Attribute{Identifier: o.ExtraAttribute, Value: json.RawMessage(`{}`)})
	}
	if o.Operation == "retrieve" {
		ids := []string{}
		for _, a := range attrs {
			ids = append(ids, a.Identifier)
		}
		return qtsp.RetrieveBody(ids, o.Mandate)
	}
	return qtsp.VerifyBody(attrs, o.Mandate)
}

// tamper changes one character of the token's signature.
func tamper(token string) string {
	b := []byte(token)
	i := len(b) - 2
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}

// Register registers at the authorization server, retrying while it is
// unavailable, for at most patience. A refusal is not retried.
func Register(ctx context.Context, c *qtsp.Client, as qtsp.AuthorizationServer, patience time.Duration) (qtsp.Registration, error) {
	deadline := time.Now().Add(patience)
	wait := 500 * time.Millisecond
	for {
		reg, err := c.Register(ctx, as)
		if err == nil || !errors.Is(err, qtsp.ErrUnavailable) || time.Now().Add(wait).After(deadline) {
			return reg, err
		}
		select {
		case <-ctx.Done():
			return reg, ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 5*time.Second)
	}
}

// Print reports the outcome and returns the exit code: 0 for an accepted
// result, 2 for an error answer of the ASIP, 3 for a result whose seal is
// invalid, 4 for a denial, 1 for any other failure. It prints no token and
// no BSN.
func Print(w io.Writer, o Outcome, err error) int {
	if o.Plan.EndpointURI != "" {
		fmt.Fprintf(w, "data service  %s (%s)\n", o.Plan.EndpointURI, o.Plan.Provider)
	}
	switch {
	case err != nil:
		fmt.Fprintf(w, "error         %v\n", err)
		return 1
	case o.Denied != nil:
		fmt.Fprintf(w, "denied        %s %s\n", o.Denied.Error, o.Denied.ErrorDescription)
		return 4
	}
	r := o.Response
	fmt.Fprintf(w, "status        %d\n", r.Status)
	if r.Status != http.StatusOK {
		var p struct {
			Title, Detail string
		}
		_ = json.Unmarshal(r.Body, &p)
		fmt.Fprintf(w, "problem       %s: %s\n", p.Title, p.Detail)
		return 2
	}
	if r.SealError != nil {
		fmt.Fprintf(w, "seal          INVALID, result not accepted: %v\n", r.SealError)
		return 3
	}
	fmt.Fprintf(w, "seal          valid, sealed by %s\n", r.Seal.Signer)
	var res struct {
		Verify []struct {
			Identifier string `json:"attributeIdentifier"`
			Result     string `json:"attributeVerificationResult"`
		} `json:"attributeVerificationResults"`
		Retrieve []struct {
			Identifier string          `json:"attributeIdentifier"`
			Value      json.RawMessage `json:"attributeValue"`
		} `json:"attributes"`
		AuthenticSource *struct {
			LegalName string `json:"legalName"`
		} `json:"authenticSource"`
	}
	_ = json.Unmarshal(r.Body, &res)
	if res.AuthenticSource != nil {
		fmt.Fprintf(w, "source        %s\n", res.AuthenticSource.LegalName)
	}
	for _, v := range res.Verify {
		fmt.Fprintf(w, "%-60s %s\n", v.Identifier, v.Result[strings.LastIndex(v.Result, "/")+1:])
	}
	for _, v := range res.Retrieve {
		fmt.Fprintf(w, "%-60s %s\n", v.Identifier, v.Value)
	}
	return 0
}
