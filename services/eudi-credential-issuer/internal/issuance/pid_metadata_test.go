package issuance_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

// metadataSource returns the income document of fakeSource with extra
// attributes.
type metadataSource struct {
	fakeSource
	extra map[string]any
}

func (s *metadataSource) Fetch(ctx context.Context, configurationID string, subject map[string]any, parameters map[string]string) ([]issuance.SourceDocument, error) {
	docs, err := s.fakeSource.Fetch(ctx, configurationID, subject, parameters)
	for k, v := range s.extra {
		docs[0].Attributes[k] = v
	}
	return docs, err
}

// issuerEnv is newEnv with the tax provider's issuer certificate naming
// uris as SAN URIs, the income configuration changed by configure, the
// configuration changed by edit, and the root of the issuer certificate.
func issuerEnv(t *testing.T, uris []string, configure func(*issuance.CredentialConfiguration), extra map[string]any, edit ...func(*issuance.Config)) (env, testpki.Cert) {
	t.Helper()
	cfg, provider := validConfig(t)
	root := testpki.NewRoot(t, "tax issuer root")
	var parsed []*url.URL
	for _, u := range uris {
		p, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, p)
	}
	issuer := root.Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "tax", Organization: []string{"tax"}},
		EKU: []asn1.ObjectIdentifier{{1, 0, 18013, 5, 1, 2}}, URIs: parsed})
	cfg.Providers[0].IssuerKey, cfg.Providers[0].IssuerChain = issuer.Key, issuer.Chain
	if configure != nil {
		configure(&cfg.Configurations[0])
	}
	for _, f := range edit {
		f(&cfg)
	}
	store := &countingStore{Store: memstore.New()}
	src := &metadataSource{extra: extra}
	svc, err := issuance.New(cfg, src, store, store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return env{svc: svc, store: store, source: &src.fakeSource, provider: provider}, root
}

// issued runs the pre-authorized code flow on the nl profile and returns
// the parsed credential.
func issued(t *testing.T, e env, root testpki.Cert) sdjwtvc.Parsed {
	t.Helper()
	return issuedAt(t, e, root, tax("nl"), false)
}

// issuedAt runs the pre-authorized code flow at target and returns the
// parsed credential. With attest the proof is a key attestation, as an
// EUDI wallet sends; otherwise a jwt proof, as the NL Wallet sends.
func issuedAt(t *testing.T, e env, root testpki.Cert, target issuance.Target, attest bool) sdjwtvc.Parsed {
	t.Helper()
	_, c := code(t, e, target.Profile, "ref")
	tok, dpopKey, err := tokenAt(t, e, target, target, c)
	if err != nil {
		t.Fatal(err)
	}
	proofs := jwtProofAt(t, e, target, target)
	if attest {
		nonce, err := e.svc.Nonce(context.Background(), target)
		if err != nil {
			t.Fatal(err)
		}
		proofs = &oid4vci.Proofs{Attestation: []string{e.provider.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, nonce, time.Now().Add(time.Hour))}}
	}
	resp, err := credentialAt(t, e, target, tok, dpopKey, proofs)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdjwtvc.Parse(resp.Credentials[0].Credential, []x5c.Anchors{x5c.NewAnchors("tax", root.Cert)}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func disclosed(p sdjwtvc.Parsed, name string) bool {
	for _, path := range p.Disclosed {
		if len(path) == 1 && path[0] == name {
			return true
		}
	}
	return false
}

// pidMetadata is the income configuration with PID metadata and the three
// claims that describe it.
func pidMetadata(c *issuance.CredentialConfiguration) {
	c.PIDMetadata = &issuance.PIDMetadata{IssuingAuthority: "GBO Demo", IssuingCountry: "NL"}
	for _, name := range []string{"issuing_authority", "issuing_country", "date_of_expiry"} {
		c.Claims = append(c.Claims, issuance.Claim{Path: []string{name}, Mandatory: true})
	}
}

// withCombined adds the combined profile "wallet", which trusts the EUDI
// wallets of the eudi profile and, without key attestation, nl.
func withCombined(nl testwallet.Provider) func(*issuance.Config) {
	return func(cfg *issuance.Config) {
		cfg.Profiles = append(cfg.Profiles, issuance.Profile{Name: "wallet", Kind: issuance.KindCombined,
			WalletProviders:         append(append([]x5c.Anchors{}, cfg.Profiles[1].WalletProviders...), nl.Anchors("nl")),
			PlainJWTWalletProviders: []string{"nl"}, KeyAttestationProviders: cfg.Profiles[1].KeyAttestationProviders,
			KeyStorage: []string{"iso_18045_high"}, UserAuthentication: []string{"iso_18045_high"}})
	}
}

// identifiers are the tax provider's credential issuer identifiers.
func identifiers(profiles ...string) []string {
	var out []string
	for _, p := range profiles {
		out = append(out, publicURL+"/tax/"+p)
	}
	return out
}

func TestIssuerClaim(t *testing.T) {
	t.Run("R4 an EUDI wallet gets iss: the identifier the issuer certificate names", func(t *testing.T) {
		e, root := issuerEnv(t, identifiers("nl", "eudi"), nil, nil)
		p := issuedAt(t, e, root, tax("eudi"), true)
		if p.Claims["iss"] != e.svc.Identifier(tax("eudi")) || disclosed(p, "iss") {
			t.Fatalf("iss %v, want %s in the clear", p.Claims["iss"], e.svc.Identifier(tax("eudi")))
		}
	})
	t.Run("R2 the NL Wallet gets no iss, even when the issuer certificate names the identifier", func(t *testing.T) {
		e, root := issuerEnv(t, identifiers("nl", "eudi"), nil, nil)
		if p := issued(t, e, root); p.Claims["iss"] != nil {
			t.Fatalf("iss %v: the NL Wallet reads iss as an attribute its preview does not have", p.Claims["iss"])
		}
	})
	t.Run("R3 on the combined profile the NL Wallet gets no iss, an EUDI wallet does", func(t *testing.T) {
		nl := testwallet.NewProvider(t, "nl provider")
		e, root := issuerEnv(t, identifiers("wallet"), nil, nil, withCombined(nl))
		if p := issuedAt(t, e, root, tax("wallet"), true); p.Claims["iss"] != e.svc.Identifier(tax("wallet")) {
			t.Fatalf("EUDI wallet: iss %v", p.Claims["iss"])
		}
		nlWallet := e
		nlWallet.provider = nl
		if p := issuedAt(t, nlWallet, root, tax("wallet"), false); p.Claims["iss"] != nil {
			t.Fatalf("NL Wallet: iss %v", p.Claims["iss"])
		}
	})
	t.Run("T3 without a SAN URI in the issuer certificate, no iss", func(t *testing.T) {
		e, root := issuerEnv(t, nil, nil, nil)
		if p := issuedAt(t, e, root, tax("eudi"), true); p.Claims["iss"] != nil {
			t.Fatalf("iss %v", p.Claims["iss"])
		}
	})
	t.Run("T3 a certificate that names another identifier gives no iss", func(t *testing.T) {
		e, root := issuerEnv(t, identifiers("nl"), nil, nil)
		if p := issuedAt(t, e, root, tax("eudi"), true); p.Claims["iss"] != nil {
			t.Fatalf("iss %v: the issuer certificate does not name %s", p.Claims["iss"], e.svc.Identifier(tax("eudi")))
		}
	})
}

// untag turns the NL Wallet's tagged attributes ({"type", "value"}) of a
// preview back into JSON values.
func untag(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	switch value := m["value"].(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range value {
			out[k] = untag(e)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, e := range value {
			out[i] = untag(e)
		}
		return out
	default:
		return value
	}
}

// R1: the NL Wallet refuses a credential that differs from its preview:
// vct, nbf, exp and every attribute must be the same.
func TestPreviewMatchesCredential(t *testing.T) {
	e, root := issuerEnv(t, identifiers("nl", "eudi"), pidMetadata, nil)
	_, c := code(t, e, "nl", "ref")
	tok, dpopKey, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := e.svc.CredentialPreview(context.Background(), tax("nl"), "Bearer "+tok.AccessToken)
	if err != nil || len(preview.CredentialPreviews) != 1 {
		t.Fatalf("preview %+v, %v", preview, err)
	}
	resp, err := credential(t, e, tok, dpopKey, jwtProof(t, e))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdjwtvc.Parse(resp.Credentials[0].Credential, []x5c.Anchors{x5c.NewAnchors("tax", root.Cert)}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{}
	for k, v := range parsed.Claims {
		// Holder binding, iat and status are not previewed.
		if k != "cnf" && k != "iat" && k != "status" {
			want[k] = v
		}
	}
	got := map[string]any{}
	for k, v := range preview.CredentialPreviews[0].Payload {
		got[k] = untag(v)
	}
	if normalize(t, got) != normalize(t, want) {
		t.Fatalf("preview %s\ncredential %s", normalize(t, got), normalize(t, want))
	}
	if _, ok := got["issuing_country"]; !ok {
		t.Fatal("the preview lacks the PID metadata")
	}
}

// normalize renders v as JSON with sorted keys and JSON numbers.
func normalize(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(back)
	return string(out)
}

func TestPIDMetadata(t *testing.T) {
	t.Run("T4 the issuer adds issuing_authority, issuing_country and date_of_expiry, each disclosable", func(t *testing.T) {
		e, root := issuerEnv(t, nil, pidMetadata, nil)
		p := issued(t, e, root)
		exp, _ := p.Claims["exp"].(float64)
		want := map[string]any{"issuing_authority": "GBO Demo", "issuing_country": "NL",
			"date_of_expiry": time.Unix(int64(exp), 0).UTC().Format(time.DateOnly)}
		for name, value := range want {
			if p.Claims[name] != value || !disclosed(p, name) {
				t.Errorf("%s: %v, want %v as a disclosure", name, p.Claims[name], value)
			}
		}
	})
	t.Run("T5 the source's attributes are issued unchanged", func(t *testing.T) {
		e, root := issuerEnv(t, nil, pidMetadata, nil)
		p := issued(t, e, root)
		address, _ := p.Claims["address"].(map[string]any)
		if p.Claims["year"] != "2025" || address["city"] != "Utrecht" {
			t.Fatalf("claims %v", p.Claims)
		}
	})
	t.Run("T5 without PID metadata, none is added", func(t *testing.T) {
		e, root := issuerEnv(t, nil, nil, nil)
		p := issued(t, e, root)
		for _, name := range []string{"issuing_authority", "issuing_country", "date_of_expiry"} {
			if _, ok := p.Claims[name]; ok {
				t.Errorf("unexpected %s", name)
			}
		}
	})
	t.Run("T4 a source that returns PID metadata itself is refused", func(t *testing.T) {
		e, _ := issuerEnv(t, nil, pidMetadata, map[string]any{"issuing_country": "BE"})
		_, err := e.svc.CreateOffer(context.Background(), issuance.OfferRequest{
			Profile: "nl", ConfigurationIDs: []string{"income"}, Subject: map[string]any{"bsn": "1"}, Reference: "ref",
		})
		if err == nil || !strings.Contains(err.Error(), "issuing_country") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestNewRefusesPIDMetadata(t *testing.T) {
	for name, m := range map[string]issuance.PIDMetadata{
		"no issuing authority":      {IssuingCountry: "NL"},
		"no issuing country":        {IssuingAuthority: "GBO Demo"},
		"country not alpha-2":       {IssuingAuthority: "GBO Demo", IssuingCountry: "NLD"},
		"country not in upper case": {IssuingAuthority: "GBO Demo", IssuingCountry: "nl"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := validConfig(t)
			pidMetadata(&cfg.Configurations[0])
			*cfg.Configurations[0].PIDMetadata = m
			store := memstore.New()
			if _, err := issuance.New(cfg, &fakeSource{}, store, store, store, time.Now); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("PID metadata without its claims", func(t *testing.T) {
		cfg, _ := validConfig(t)
		cfg.Configurations[0].PIDMetadata = &issuance.PIDMetadata{IssuingAuthority: "GBO Demo", IssuingCountry: "NL"}
		store := memstore.New()
		if _, err := issuance.New(cfg, &fakeSource{}, store, store, store, time.Now); err == nil || !strings.Contains(err.Error(), "needs a claim for") {
			t.Fatalf("got %v", err)
		}
	})
}
