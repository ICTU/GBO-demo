package harness

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// fakeAS shows a request page and a DigiD mock, and redirects to the client
// with the request's state, or with returnState when set.
func fakeAS(t *testing.T, returnState string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	redirect := func(w http.ResponseWriter, r *http.Request, q url.Values) {
		c, _ := r.Cookie("tx")
		state := c.Value
		if returnState != "" {
			state = returnState
		}
		q.Set("state", state)
		http.Redirect(w, r, "https://qtsp.test/callback?"+q.Encode(), http.StatusFound)
	}
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("prompt") == "deny-now" {
			http.Redirect(w, r, "https://qtsp.test/callback?error=invalid_target&state="+r.URL.Query().Get("state"), http.StatusFound)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "tx", Value: r.URL.Query().Get("state")})
		_, _ = io.WriteString(w, `<form method="post" action="/authorize/decision"><input type="hidden" name="transaction" value="t1">`)
	})
	mux.HandleFunc("POST /authorize/decision", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("tx"); err != nil || r.FormValue("transaction") != "t1" {
			http.Error(w, "bad decision", http.StatusBadRequest)
			return
		}
		switch r.FormValue("decision") {
		case "deny":
			redirect(w, r, url.Values{"error": {"access_denied"}})
		case "digid":
			http.Redirect(w, r, "/authorize/digid?transaction=t1", http.StatusSeeOther)
		default:
			http.Error(w, "bad decision", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /authorize/digid", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<form method="post" action="/authorize/digid"><input type="hidden" name="transaction" value="t1">`)
	})
	mux.HandleFunc("POST /authorize/digid", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("tx"); err != nil || r.FormValue("transaction") != "t1" || r.FormValue("bsn") != "999991772" {
			http.Error(w, "bad login", http.StatusBadRequest)
			return
		}
		redirect(w, r, url.Values{"code": {"c1"}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDecide(t *testing.T) {
	as := fakeAS(t, "")
	ctx := context.Background()
	cb, err := Decide(ctx, http.DefaultClient, as.URL+"/authorize?state=s1", "999991772", true)
	if err != nil || cb.Code != "c1" || cb.State != "s1" {
		t.Fatalf("approve: %+v, %v", cb, err)
	}
	cb, err = Decide(ctx, http.DefaultClient, as.URL+"/authorize?state=s2", "999991772", false)
	if err != nil || cb.Error != "access_denied" || cb.Code != "" {
		t.Fatalf("deny: %+v, %v", cb, err)
	}
	cb, err = Decide(ctx, http.DefaultClient, as.URL+"/authorize?state=s3&prompt=deny-now", "999991772", true)
	if err != nil || cb.Error != "invalid_target" {
		t.Fatalf("refused request: %+v, %v", cb, err)
	}
}

// A redirect with another state does not belong to this request.
func TestDecideRefusesAnotherState(t *testing.T) {
	if _, err := Decide(context.Background(), http.DefaultClient, fakeAS(t, "forged").URL+"/authorize?state=s1", "999991772", true); err == nil {
		t.Fatal("err = nil, want another state refused")
	}
}
