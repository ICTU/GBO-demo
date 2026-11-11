package testloginhttp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/testloginhttp"
)

func TestWalletReturnPolicy(t *testing.T) {
	// The browser must be allowed to follow the form's callback into the configured
	// wallet, without admitting arbitrary schemes, origins or injected directives.
	handler := testloginhttp.Handler(nil, "eu.europa.ec.euidi://authorization", "https://wallet.test/callback", "https://bad.test/; form-action *", "javascript:alert(1)")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodOptions, "/test-persons", nil))
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "form-action 'self' eu.europa.ec.euidi://authorization https://wallet.test/callback;") || strings.Contains(policy, "bad.test") || strings.Contains(policy, "javascript:") {
		t.Fatalf("unexpected form return policy: %s", policy)
	}
	if !strings.Contains(policy, "default-src 'none'") || !strings.Contains(policy, "frame-ancestors 'none'") {
		t.Fatal("unrelated browser restrictions lost")
	}
}
