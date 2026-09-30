// Package httpapi is the driving adapter of the mock: it serves the three
// interfaces over HTTP and translates between JSON and the two cores. Every
// rule lives in those cores; a handler here reads a request, calls one use
// case and writes the answer.
//
//	first.go       what is left of the first interface
//	bsnk.go        BSNk's own interface, as JSON instead of SOAP
//	decryption.go  the interface of the decryption component a party runs
package httpapi

import (
	"encoding/json"
	"net/http"

	"bsnk-mock/internal/polymorphic"
)

// NewMux builds the routing tree.
//
// randomizeDefault applies when a request to BSNk does not say, with the
// query parameter randomize, whether it wants values that differ every time.
func NewMux(mock *polymorphic.Mock, randomizeDefault bool) *http.ServeMux {
	mux := NewDecryptionMux()

	mux.HandleFunc("/pseudonymize", handlePseudonymize())

	b := bsnk{mock: mock, randomizeDefault: randomizeDefault}
	mux.HandleFunc("/v2/activate", post(b.activate))
	mux.HandleFunc("/v2/transform", post(b.transform))
	mux.HandleFunc("/v2/provide-dv-keys", post(b.provideDVKeys))
	mux.HandleFunc("/v2/bsn-authorisation-list", get(b.authorisationList))
	mux.HandleFunc("/v2/scheme-keys", get(b.schemeKeys))

	return mux
}

// NewDecryptionMux builds the routing tree of the decryption component alone:
// what a party runs for itself. It serves nothing of BSNk, so that a party
// which is given only this cannot be calling BSNk.
func NewDecryptionMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/signed-encrypted-identity", decryption(decryptIdentity))
	mux.HandleFunc("/signed-encrypted-pseudonym", decryption(decryptPseudonym))

	return mux
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// allow answers a preflight and refuses any method but the wanted one. It
// reports whether the handler should go on.
func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	corsHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	if r.Method != method {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return false
	}
	return true
}
