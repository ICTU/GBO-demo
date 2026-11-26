package democontrol_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/democontrol"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

func config() issuance.Config {
	return issuance.Config{
		BatchSize: 4,
		Providers: []issuance.Provider{{Name: "tax"}, {Name: "civil"}},
		Configurations: []issuance.CredentialConfiguration{
			{ID: "income-2024", Provider: "tax", Display: []issuance.Display{{Name: "Inkomensverklaring 2024", Locale: "nl"}}},
			{ID: "income-2025", Provider: "tax", Display: []issuance.Display{{Name: "Inkomensverklaring 2025", Locale: "nl"}}},
			{ID: "death", Provider: "civil"},
		},
	}
}

func newOverrides() *democontrol.Overrides {
	cfg := config()
	return democontrol.NewOverrides(cfg, issuance.ConfiguredSettings(cfg))
}

func call(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: %d %q is not JSON", method, path, rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func view(t *testing.T, h http.Handler) democontrol.View {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/settings", nil))
	var v democontrol.View
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
		t.Fatalf("GET /demo/settings = %d %s", rec.Code, rec.Body.String())
	}
	return v
}

func provider(v democontrol.View, name string) democontrol.ProviderSettings {
	for _, p := range v.Providers {
		if p.Name == name {
			return p
		}
	}
	return democontrol.ProviderSettings{}
}

func configuration(v democontrol.View, id string) democontrol.ConfigurationSettings {
	for _, c := range v.Configurations {
		if c.ID == id {
			return c
		}
	}
	return democontrol.ConfigurationSettings{}
}

// A1, A6: without changes, and after a restart, the configuration's
// settings are in force.
func TestSettingsOfTheConfiguration(t *testing.T) {
	v := view(t, handler(newOverrides()))
	if p := provider(v, "tax"); p.BatchSize != 4 || p.Overridden {
		t.Errorf("tax = %+v, want the configured batch size 4", p)
	}
	if c := configuration(v, "income-2024"); c.ReusePolicy != nil || c.BatchSize != 4 || c.Name != "Inkomensverklaring 2024" {
		t.Errorf("income-2024 = %+v, want no policy and the provider's batch size", c)
	}
}

// A2, A3, A5: a provider's batch size and a configuration's reuse policy are
// set, in force at once, and reset.
func TestChangeSettings(t *testing.T) {
	o := newOverrides()
	h := handler(o)
	if status, body := call(t, h, http.MethodPut, "/demo/settings/providers/tax", `{"batch_size":6}`); status != http.StatusOK {
		t.Fatalf("A2: %d %v", status, body)
	}
	if o.BatchSize("tax") != 6 || o.BatchSize("civil") != 4 {
		t.Errorf("A2: batch sizes tax %d, civil %d; want 6 and 4", o.BatchSize("tax"), o.BatchSize("civil"))
	}
	if c := configuration(view(t, h), "income-2025"); c.BatchSize != 6 {
		t.Errorf("A2: income-2025 shows batch size %d, want the provider's 6", c.BatchSize)
	}

	status, body := call(t, h, http.MethodPut, "/demo/settings/configurations/income-2024",
		`{"reuse_policy":{"method":"once_only","batch_size":3,"reissue_trigger_unused":1}}`)
	if status != http.StatusOK {
		t.Fatalf("A3: %d %v", status, body)
	}
	if p, ok := o.ReusePolicy("income-2024"); !ok || p.BatchSize != 3 || p.ReissueTriggerUnused != 1 || p.Method != issuance.ReuseOnceOnly {
		t.Errorf("A3: income-2024 policy = %+v %v", p, ok)
	}
	if _, ok := o.ReusePolicy("income-2025"); ok {
		t.Error("A3: income-2025 got a policy too")
	}
	if c := configuration(view(t, h), "income-2024"); c.BatchSize != 3 || !c.Overridden {
		t.Errorf("A3: income-2024 = %+v, want the policy's batch size 3", c)
	}

	if status, body := call(t, h, http.MethodDelete, "/demo/settings/configurations/income-2024", ""); status != http.StatusOK {
		t.Fatalf("A5: %d %v", status, body)
	}
	if _, ok := o.ReusePolicy("income-2024"); ok {
		t.Error("A5: the policy is still in force")
	}
	if status, body := call(t, h, http.MethodDelete, "/demo/settings/providers/tax", ""); status != http.StatusOK || o.BatchSize("tax") != 4 {
		t.Fatalf("reset tax: %d %v, batch size %d", status, body, o.BatchSize("tax"))
	}
}

// A4: the issuer refuses an invalid setting and says why; nothing changes.
func TestInvalidSettingsAreRefused(t *testing.T) {
	o := newOverrides()
	h := handler(o)
	for name, tc := range map[string]struct {
		method, path, body string
		status             int
		code               string
	}{
		"policy batch size below 2": {http.MethodPut, "/demo/settings/configurations/income-2024",
			`{"reuse_policy":{"method":"once_only","batch_size":1,"reissue_trigger_unused":0}}`, http.StatusBadRequest, "invalid_setting"},
		"negative trigger": {http.MethodPut, "/demo/settings/configurations/income-2024",
			`{"reuse_policy":{"method":"once_only","batch_size":3,"reissue_trigger_unused":-1}}`, http.StatusBadRequest, "invalid_setting"},
		"trigger equal to the batch size": {http.MethodPut, "/demo/settings/configurations/income-2024",
			`{"reuse_policy":{"method":"once_only","batch_size":3,"reissue_trigger_unused":3}}`, http.StatusBadRequest, "invalid_setting"},
		"unsupported method": {http.MethodPut, "/demo/settings/configurations/income-2024",
			`{"reuse_policy":{"method":"rotating-batch","batch_size":3}}`, http.StatusBadRequest, "invalid_setting"},
		"provider batch size 0": {http.MethodPut, "/demo/settings/providers/tax", `{"batch_size":0}`, http.StatusBadRequest, "invalid_setting"},
		"no batch size":         {http.MethodPut, "/demo/settings/providers/tax", `{}`, http.StatusBadRequest, "invalid_setting"},
		"unknown field":         {http.MethodPut, "/demo/settings/providers/tax", `{"batch":6}`, http.StatusBadRequest, "invalid_request"},
		"unknown provider":      {http.MethodPut, "/demo/settings/providers/nobody", `{"batch_size":6}`, http.StatusNotFound, "unknown_provider"},
		"unknown configuration": {http.MethodPut, "/demo/settings/configurations/nothing",
			`{"reuse_policy":{"method":"once_only","batch_size":3,"reissue_trigger_unused":1}}`, http.StatusNotFound, "unknown_configuration"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := call(t, h, tc.method, tc.path, tc.body)
			if status != tc.status || body["error"] != tc.code || body["error_description"] == "" {
				t.Errorf("%d %v, want %d %s with a reason", status, body, tc.status, tc.code)
			}
		})
	}
	v := view(t, h)
	if p := provider(v, "tax"); p.Overridden {
		t.Errorf("a refused change was applied: %+v", p)
	}
	if c := configuration(v, "income-2024"); c.ReusePolicy != nil {
		t.Errorf("a refused change was applied: %+v", c)
	}
}

func handler(o *democontrol.Overrides) http.Handler {
	return democontrol.Handler(o)
}
