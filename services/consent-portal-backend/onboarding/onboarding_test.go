package onboarding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

func client(t *testing.T, handler http.HandlerFunc) Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return Client{Base: server.URL, Caller: upstream.Caller{Client: &http.Client{Timeout: 5 * time.Second}}}
}

func TestRegistrationReadsTheParticipant(t *testing.T) {
	var path string
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"peer_id": "99999999900000000300",
			"name": "Demo Hypotheekverlener BV",
			"active": true,
			"services": [{"service": "aangiftegegevens", "purpose": "Hypotheek-aanvraag"}],
			"return_locations": ["http://localhost:9001/return"]
		}`)
	})
	registration, err := c.Registration(context.Background(), "99999999900000000300")
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	if path != "/internal/consent-portal/participants/99999999900000000300" {
		t.Fatalf("path = %s", path)
	}
	want := consent.Registration{
		Name:            "Demo Hypotheekverlener BV",
		Active:          true,
		Services:        []consent.RegisteredService{{Service: "aangiftegegevens", Purpose: "Hypotheek-aanvraag"}},
		ReturnLocations: []string{"http://localhost:9001/return"},
	}
	if fmt.Sprint(registration) != fmt.Sprint(want) {
		t.Fatalf("registration = %+v, want %+v", registration, want)
	}
}

func TestRegistrationWithoutRegisteredServices(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"peer_id": "99999999900000000500", "name": "Demo Incassobureau BV", "active": false, "services": [], "return_locations": []}`)
	})
	registration, err := c.Registration(context.Background(), "99999999900000000500")
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	if registration.Active || len(registration.Services) != 0 || len(registration.ReturnLocations) != 0 {
		t.Fatalf("registration = %+v", registration)
	}
}

func TestRegistrationOfAnUnknownPeer(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": "participant not found"}`)
	})
	_, err := c.Registration(context.Background(), "99999999900000009999")
	if !errors.Is(err, consent.ErrUnknownParty) {
		t.Fatalf("err = %v, want ErrUnknownParty", err)
	}
}

func TestRegistrationWhenTheRegisterFails(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"500": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": "register unavailable"}`)
		},
		"503": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"not JSON": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `<html>`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client(t, handler).Registration(context.Background(), "99999999900000000300")
			if err == nil || errors.Is(err, consent.ErrUnknownParty) {
				t.Fatalf("err = %v, want a failure that is not ErrUnknownParty", err)
			}
		})
	}
}

func TestRegistrationWhenTheRegisterIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	base := server.URL
	server.Close()
	c := Client{Base: base, Caller: upstream.Caller{Client: &http.Client{Timeout: time.Second}}}
	_, err := c.Registration(context.Background(), "99999999900000000300")
	if err == nil || errors.Is(err, consent.ErrUnknownParty) {
		t.Fatalf("err = %v, want a failure that is not ErrUnknownParty", err)
	}
}

func TestRegistrationEscapesThePeerID(t *testing.T) {
	var rawPath string
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		rawPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNotFound)
	})
	_, _ = c.Registration(context.Background(), "../openftv/participants")
	if rawPath != "/internal/consent-portal/participants/..%2Fopenftv%2Fparticipants" {
		t.Fatalf("path = %s", rawPath)
	}
}
