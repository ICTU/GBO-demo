// Package harness plays the user at the test-person chooser of the GBO demo's
// authorization server, so the CLI and CI can run the flow without a browser.
// It is a test harness: a real user identifies and decides in a browser, and
// the chooser itself is a stub (no identity is checked).
package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
)

// Callback is what the user's browser brings back to the QTSP.
type Callback struct {
	Code, State, Error, ErrorDescription string
}

var (
	transactionField = regexp.MustCompile(`name="transaction" value="([^"]+)"`)
	formAction       = regexp.MustCompile(`<form method="post" action="([^"]+)"`)
)

// Decide opens authorizationURL, picks the test person and approves or
// denies, and returns the redirect back to the QTSP. A redirect with another
// state than the request's is an error. client carries the transport (and
// any URL map); the harness adds a cookie jar, because the chooser binds the
// decision to the browser.
func Decide(ctx context.Context, client *http.Client, authorizationURL, person string, approve bool) (Callback, error) {
	target, err := url.Parse(authorizationURL)
	if err != nil {
		return Callback{}, err
	}
	state := target.Query().Get("state")
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{
		Transport:     client.Transport,
		Timeout:       client.Timeout,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizationURL, nil)
	if err != nil {
		return Callback{}, err
	}
	resp, err := browser.Do(req)
	if err != nil {
		return Callback{}, err
	}
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusFound {
		return callback(resp.Header.Get("Location"), state)
	}
	tx, action := transactionField.FindSubmatch(page), formAction.FindSubmatch(page)
	if resp.StatusCode != http.StatusOK || tx == nil || action == nil {
		return Callback{}, fmt.Errorf("the authorization server showed no chooser (status %d)", resp.StatusCode)
	}
	post, err := target.Parse(string(action[1]))
	if err != nil {
		return Callback{}, err
	}
	decision := "deny"
	if approve {
		decision = "approve"
	}
	form := url.Values{"transaction": {string(tx[1])}, "person": {person}, "decision": {decision}}
	resp, err = browser.PostForm(post.String(), form)
	if err != nil {
		return Callback{}, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return Callback{}, fmt.Errorf("the decision was not accepted (status %d)", resp.StatusCode)
	}
	return callback(resp.Header.Get("Location"), state)
}

func callback(location, state string) (Callback, error) {
	u, err := url.Parse(location)
	if err != nil || location == "" {
		return Callback{}, errors.New("the authorization server did not redirect back")
	}
	q := u.Query()
	if q.Get("state") != state {
		return Callback{}, errors.New("the authorization server returned another state")
	}
	return Callback{Code: q.Get("code"), State: q.Get("state"), Error: q.Get("error"), ErrorDescription: q.Get("error_description")}, nil
}
