package issuance_test

// The batch settings in force: a batch size per provider, published as
// batch_credential_issuance, and a reuse policy per credential
// configuration, published as credential_reuse_policy (ETSI TS 119 472-3
// clause 4.2.4). Cases A (setting) and B (enforcing) of the batch-size slice
// of the credential lifecycle (#745).

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

// fakeSettings are settings a test changes between requests.
type fakeSettings struct {
	mu       sync.Mutex
	batch    map[string]int
	policies map[string]issuance.ReusePolicy
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{batch: map[string]int{}, policies: map[string]issuance.ReusePolicy{}}
}

func (s *fakeSettings) BatchSize(provider string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.batch[provider]; ok {
		return n
	}
	return issuance.DefaultBatchSize
}

func (s *fakeSettings) ReusePolicy(configurationID string) (issuance.ReusePolicy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[configurationID]
	return p, ok
}

func (s *fakeSettings) setBatch(provider string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batch[provider] = n
}

// setPolicy sets the policy of a configuration; nil removes it.
func (s *fakeSettings) setPolicy(configurationID string, p *issuance.ReusePolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == nil {
		delete(s.policies, configurationID)
		return
	}
	s.policies[configurationID] = *p
}

func onceOnly(batch, trigger int) *issuance.ReusePolicy {
	return &issuance.ReusePolicy{Method: issuance.ReuseOnceOnly, BatchSize: batch, ReissueTriggerUnused: trigger}
}

// settingsEnv is newEnv with fake settings and a second configuration of
// the tax provider, "income-2025", so two types share one provider.
func settingsEnv(t *testing.T) (env, *fakeSettings) {
	t.Helper()
	cfg, provider := validConfig(t)
	second := cfg.Configurations[0]
	second.ID, second.Scope = "income-2025", "income-2025"
	cfg.Configurations = append(cfg.Configurations, second)
	settings := newFakeSettings()
	cfg.Settings = settings
	store := &countingStore{Store: memstore.New()}
	src := &fakeSource{}
	svc, err := issuance.New(cfg, src, store, store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return env{svc: svc, store: store, source: src, provider: provider}, settings
}

// metadataOf returns the issuer metadata of target as generic JSON.
func metadataOf(t *testing.T, e env, target issuance.Target) map[string]any {
	t.Helper()
	md, err := e.svc.IssuerMetadataJSON(target)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(md)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// batchSizeOf returns the published batch_credential_issuance.batch_size,
// or 0 when it is left out.
func batchSizeOf(md map[string]any) int {
	b, ok := md["batch_credential_issuance"].(map[string]any)
	if !ok {
		return 0
	}
	n, _ := b["batch_size"].(float64)
	return int(n)
}

// reusePolicyOf returns the credential_reuse_policy of a configuration, or
// nil when it is left out.
func reusePolicyOf(t *testing.T, md map[string]any, configurationID string) any {
	t.Helper()
	configs, _ := md["credential_configurations_supported"].(map[string]any)
	c, ok := configs[configurationID].(map[string]any)
	if !ok {
		t.Fatalf("configuration %s missing from %v", configurationID, configs)
	}
	cm, _ := c["credential_metadata"].(map[string]any)
	return cm["credential_reuse_policy"]
}

func civil(profile string) issuance.Target {
	return issuance.Target{Provider: "civil", Profile: profile}
}

// A1, A2: the batch size is per provider, and read on every request.
func TestBatchSizePerProvider(t *testing.T) {
	e, settings := settingsEnv(t)
	for _, profile := range []string{"nl", "eudi"} {
		if got := batchSizeOf(metadataOf(t, e, tax(profile))); got != issuance.DefaultBatchSize {
			t.Errorf("A1 %s: batch_size = %d, want the default %d", profile, got, issuance.DefaultBatchSize)
		}
	}
	settings.setBatch("tax", 6)
	for _, profile := range []string{"nl", "eudi"} {
		if got := batchSizeOf(metadataOf(t, e, tax(profile))); got != 6 {
			t.Errorf("A2 %s: tax batch_size = %d, want 6", profile, got)
		}
		if got := batchSizeOf(metadataOf(t, e, civil(profile))); got != issuance.DefaultBatchSize {
			t.Errorf("A2 %s: civil batch_size = %d, want %d", profile, got, issuance.DefaultBatchSize)
		}
	}
	settings.setBatch("tax", 1)
	if md := metadataOf(t, e, tax("eudi")); md["batch_credential_issuance"] != nil {
		t.Errorf("batch size 1: batch_credential_issuance = %v, want none", md["batch_credential_issuance"])
	}
}

// A3, A5: the reuse policy is per credential configuration, published only
// on the profiles that serve the EUDI wallets, and gone once removed.
func TestReusePolicyPerConfiguration(t *testing.T) {
	e, settings := settingsEnv(t)
	settings.setPolicy("income", onceOnly(3, 1))
	want := map[string]any{
		"id": "arf_annex_ii",
		"options": []any{map[string]any{
			"details":                []any{"once_only"},
			"batch_size":             float64(3),
			"reissue_trigger_unused": float64(1),
		}},
	}
	md := metadataOf(t, e, tax("eudi"))
	if got := reusePolicyOf(t, md, "income"); !reflect.DeepEqual(got, want) {
		t.Errorf("A3: income credential_reuse_policy = %v, want %v", got, want)
	}
	if got := reusePolicyOf(t, md, "income-2025"); got != nil {
		t.Errorf("A3: income-2025 of the same provider has credential_reuse_policy %v, want none", got)
	}
	if got := batchSizeOf(md); got != issuance.DefaultBatchSize {
		t.Errorf("A3: the provider's batch_size = %d, want %d next to the policy", got, issuance.DefaultBatchSize)
	}
	if got := reusePolicyOf(t, metadataOf(t, e, tax("nl")), "income"); got != nil {
		t.Errorf("the nl profile publishes credential_reuse_policy %v, want none", got)
	}
	settings.setPolicy("income", nil)
	if got := reusePolicyOf(t, metadataOf(t, e, tax("eudi")), "income"); got != nil {
		t.Errorf("A5: removed policy still published: %v", got)
	}
}

// A4: the issuer decides what a valid reuse policy is (ETSI TS 119 472-3
// clauses 4.2.4.2-07 to 4.2.4.2-11).
func TestReusePolicyValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		policy issuance.ReusePolicy
		valid  bool
	}{
		"once_only, batch 3, trigger 1":   {*onceOnly(3, 1), true},
		"once_only, batch 2, trigger 0":   {*onceOnly(2, 0), true},
		"batch size below 2":              {*onceOnly(1, 0), false},
		"negative trigger":                {*onceOnly(3, -1), false},
		"trigger equal to the batch size": {*onceOnly(3, 3), false},
		"trigger above the batch size":    {*onceOnly(3, 4), false},
		"a method not supported yet":      {issuance.ReusePolicy{Method: "rotating-batch", BatchSize: 3}, false},
		"no method":                       {issuance.ReusePolicy{BatchSize: 3, ReissueTriggerUnused: 1}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.policy.Validate(); (err == nil) != tc.valid {
				t.Errorf("Validate() = %v, want valid %v", err, tc.valid)
			}
		})
	}
	for size, valid := range map[int]bool{-1: false, 0: false, 1: true, 2: true, 10: true} {
		if err := issuance.ValidateBatchSize(size); (err == nil) != valid {
			t.Errorf("ValidateBatchSize(%d) = %v, want valid %v", size, err, valid)
		}
	}
}

// attestedKeys returns a key attestation proof for n new keys, with a
// c_nonce of target.
func attestedKeys(t *testing.T, e env, target issuance.Target, n int) *oid4vci.Proofs {
	t.Helper()
	nonce, err := e.svc.Nonce(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]*ecdsa.PrivateKey, n)
	for i := range keys {
		keys[i] = testpki.NewKey(t)
	}
	return &oid4vci.Proofs{Attestation: []string{e.provider.KeyAttestation(t, keys, nonce, time.Now().Add(time.Hour))}}
}

// jwtProofs returns n jwt proofs, each with a new key, for target.
func jwtProofs(t *testing.T, e env, target issuance.Target, n int) *oid4vci.Proofs {
	t.Helper()
	nonce, err := e.svc.Nonce(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	proofs := &oid4vci.Proofs{}
	for range n {
		proofs.JWT = append(proofs.JWT, testwallet.JWTProof(t, testpki.NewKey(t), e.svc.Identifier(target), nonce, ""))
	}
	return proofs
}

// issueKeys redeems a new offer for configuration on profile and asks for
// one credential per key of proofs(target).
func issueKeys(t *testing.T, e env, profile, configuration string, proofs func(issuance.Target) *oid4vci.Proofs) (oid4vci.CredentialResponse, error) {
	t.Helper()
	_, c := code(t, e, profile, "", configuration)
	tok, dpopKey, err := token(t, e, profile, c)
	if err != nil {
		t.Fatal(err)
	}
	return credentialAt(t, e, tax(profile), tok, dpopKey, proofs(tax(profile)))
}

// B1, B2, B3: the batch size in force is the policy's for a configuration
// with one, the provider's otherwise. Fewer keys give fewer credentials;
// more keys are refused.
func TestBatchSizeInForce(t *testing.T) {
	e, settings := settingsEnv(t)
	settings.setBatch("tax", 6)
	settings.setPolicy("income", onceOnly(3, 1))
	attested := func(n int) func(issuance.Target) *oid4vci.Proofs {
		return func(target issuance.Target) *oid4vci.Proofs { return attestedKeys(t, e, target, n) }
	}
	for name, tc := range map[string]struct {
		configuration string
		keys          int
		want          int // 0: refused with invalid_proof
	}{
		"B1 policy: as many keys as the batch size": {"income", 3, 3},
		"B2 policy: fewer keys":                     {"income", 2, 2},
		"B2 policy: one key":                        {"income", 1, 1},
		"B3 policy: more keys":                      {"income", 4, 0},
		"B1 provider: as many keys as its batch":    {"income-2025", 6, 6},
		"B3 provider: more keys":                    {"income-2025", 7, 0},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := issueKeys(t, e, "eudi", tc.configuration, attested(tc.keys))
			if tc.want == 0 {
				refusedWith(t, err, oid4vci.ErrInvalidProof)
				return
			}
			if err != nil || len(resp.Credentials) != tc.want {
				t.Fatalf("credentials = %d, %v; want %d", len(resp.Credentials), err, tc.want)
			}
		})
	}
}

// The nl profile does not publish the policy, so the NL Wallet is held to
// the provider's batch size.
func TestBatchSizeOnTheNLProfileIgnoresThePolicy(t *testing.T) {
	e, settings := settingsEnv(t)
	settings.setPolicy("income", onceOnly(2, 1))
	resp, err := issueKeys(t, e, "nl", "income", func(target issuance.Target) *oid4vci.Proofs {
		return jwtProofs(t, e, target, issuance.DefaultBatchSize)
	})
	if err != nil || len(resp.Credentials) != issuance.DefaultBatchSize {
		t.Fatalf("credentials = %d, %v; want %d", len(resp.Credentials), err, issuance.DefaultBatchSize)
	}
}

// B4: the setting in force at the credential request counts, not the one at
// the offer or the token.
func TestBatchSizeChangedAfterTheToken(t *testing.T) {
	e, settings := settingsEnv(t)
	settings.setPolicy("income", onceOnly(4, 1))
	_, c := code(t, e, "eudi", "", "income")
	tok, dpopKey, err := token(t, e, "eudi", c)
	if err != nil {
		t.Fatal(err)
	}
	settings.setPolicy("income", onceOnly(2, 1))
	_, err = credentialAt(t, e, tax("eudi"), tok, dpopKey, attestedKeys(t, e, tax("eudi"), 3))
	refusedWith(t, err, oid4vci.ErrInvalidProof)
}

// B5: a re-issuance with a refresh token follows the setting in force at
// the refresh.
func TestBatchSizeAtRefresh(t *testing.T) {
	e, settings := settingsEnv(t)
	settings.setPolicy("income", onceOnly(2, 1))
	_, c := code(t, e, "eudi", "", "income")
	tok, dpopKey, err := token(t, e, "eudi", c)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := credentialAt(t, e, tax("eudi"), tok, dpopKey, attestedKeys(t, e, tax("eudi"), 2)); err != nil || len(resp.Credentials) != 2 {
		t.Fatalf("first issuance = %d, %v", len(resp.Credentials), err)
	}
	settings.setPolicy("income", onceOnly(3, 1))
	again := refresh(t, e, tax("eudi"), tok.RefreshToken, dpopKey)
	resp, err := credentialAt(t, e, tax("eudi"), again, dpopKey, attestedKeys(t, e, tax("eudi"), 3))
	if err != nil || len(resp.Credentials) != 3 {
		t.Fatalf("re-issuance = %d, %v; want 3", len(resp.Credentials), err)
	}
}

// refresh redeems a refresh token at target with the DPoP key it is bound
// to.
func refresh(t *testing.T, e env, target issuance.Target, refreshToken string, dpopKey *ecdsa.PrivateKey) oid4vci.TokenResponse {
	t.Helper()
	ctx := context.Background()
	wiaKey := testpki.NewKey(t)
	challenge, err := e.svc.Challenge(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	id := e.svc.Identifier(target)
	resp, err := e.svc.Token(ctx, issuance.TokenRequest{
		Target: target, GrantType: oid4vci.GrantRefreshToken, RefreshToken: refreshToken,
		ClientAttestation:    e.provider.WIA(t, client, wiaKey, time.Now().Add(time.Hour)),
		ClientAttestationPoP: testwallet.PoP(t, wiaKey, client, id, challenge),
		DPoP:                 []string{testwallet.DPoP(t, dpopKey, "POST", id+"/token", "")},
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return resp
}
