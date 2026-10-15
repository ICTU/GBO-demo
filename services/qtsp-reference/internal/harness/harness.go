// Package harness plays the user at the authorization server of the GBO demo,
// so the CLI and CI can run the flow without a browser: on the request page
// it logs in with the DigiD mock, or denies. It is a test harness: a real
// user identifies and decides in a browser, and the DigiD mock itself checks
// nothing.
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
	"strings"
)

// Callback is what the user's browser brings back to the QTSP.
type Callback struct {
	Code, State, Error, ErrorDescription string
}

var (
	transactionField = regexp.MustCompile(`name="transaction" value="([^"]+)"`)
	formAction       = regexp.MustCompile(`<form method="post" action="([^"]+)"`)
)

// Decide opens authorizationURL and, on the request page, logs in with the
// DigiD mock with bsn, which approves the request, or denies it. It returns
// the redirect back to the QTSP; a redirect with another state than the
// request's is an error. client carries the transport (and any URL map); the
// harness adds a cookie jar, because the authorization server binds the
// request to the browser.
func Decide(ctx context.Context, client *http.Client, authorizationURL, bsn string, approve bool) (Callback, error) {
	target, err := url.Parse(authorizationURL)
	if err != nil {
		return Callback{}, err
	}
	state := target.Query().Get("state")
	jar, _ := cookiejar.New(nil)
	b := &browser{http: &http.Client{
		Transport:     client.Transport,
		Timeout:       client.Timeout,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	resp, page, err := b.do(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Callback{}, err
	}
	if resp.StatusCode == http.StatusFound {
		return callback(resp.Header.Get("Location"), state)
	}
	choice := "deny"
	if approve {
		choice = "digid"
	}
	resp, _, err = b.submit(ctx, target, resp.StatusCode, page, "the request page", url.Values{"decision": {choice}})
	if err != nil {
		return Callback{}, err
	}
	if approve {
		if resp.StatusCode != http.StatusSeeOther {
			return Callback{}, fmt.Errorf("choosing DigiD was not accepted (status %d)", resp.StatusCode)
		}
		login, err := target.Parse(resp.Header.Get("Location"))
		if err != nil {
			return Callback{}, err
		}
		resp, page, err = b.do(ctx, http.MethodGet, login, nil)
		if err != nil {
			return Callback{}, err
		}
		resp, _, err = b.submit(ctx, login, resp.StatusCode, page, "the DigiD mock", url.Values{"username": {"harness"}, "bsn": {bsn}})
		if err != nil {
			return Callback{}, err
		}
	}
	if resp.StatusCode != http.StatusFound {
		return Callback{}, fmt.Errorf("the authorization server did not complete the request (status %d)", resp.StatusCode)
	}
	return callback(resp.Header.Get("Location"), state)
}

// browser is the harness's user agent: it keeps cookies, follows no
// redirects, and reads pages.
type browser struct{ http *http.Client }

func (b *browser) do(ctx context.Context, method string, target *url.URL, form url.Values) (*http.Response, string, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, "", err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, string(raw), nil
}

// submit posts the form of page, at base, with the page's transaction and
// fields.
func (b *browser) submit(ctx context.Context, base *url.URL, status int, page, name string, fields url.Values) (*http.Response, string, error) {
	tx, action := transactionField.FindStringSubmatch(page), formAction.FindStringSubmatch(page)
	if status != http.StatusOK || tx == nil || action == nil {
		return nil, "", fmt.Errorf("the authorization server showed no form on %s (status %d)", name, status)
	}
	post, err := base.Parse(action[1])
	if err != nil {
		return nil, "", err
	}
	fields.Set("transaction", tx[1])
	return b.do(ctx, http.MethodPost, post, fields)
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
