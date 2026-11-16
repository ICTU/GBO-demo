package issuance_test

import (
	"context"
	"crypto/x509/pkix"
	"encoding/asn1"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/testpki"
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
// uris as SAN URIs, the income configuration changed by configure, and the
// root of the issuer certificate.
func issuerEnv(t *testing.T, uris []string, configure func(*issuance.CredentialConfiguration), extra map[string]any) (env, testpki.Cert) {
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
	_, c := code(t, e, "nl", "ref")
	tok, dpopKey, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := credential(t, e, tok, dpopKey, jwtProof(t, e))
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

func TestIssuerClaim(t *testing.T) {
	t.Run("T1 iss is the credential issuer identifier the issuer certificate names", func(t *testing.T) {
		e, root := issuerEnv(t, []string{publicURL + "/tax/nl", publicURL + "/tax/eudi"}, nil, nil)
		p := issued(t, e, root)
		if p.Claims["iss"] != e.svc.Identifier(tax("nl")) || disclosed(p, "iss") {
			t.Fatalf("iss %v, want %s in the clear", p.Claims["iss"], e.svc.Identifier(tax("nl")))
		}
	})
	t.Run("T3 without a SAN URI in the issuer certificate, no iss", func(t *testing.T) {
		e, root := issuerEnv(t, nil, nil, nil)
		if p := issued(t, e, root); p.Claims["iss"] != nil {
			t.Fatalf("iss %v", p.Claims["iss"])
		}
	})
	t.Run("T3 a certificate that names another identifier gives no iss", func(t *testing.T) {
		e, root := issuerEnv(t, []string{publicURL + "/tax/eudi"}, nil, nil)
		if p := issued(t, e, root); p.Claims["iss"] != nil {
			t.Fatalf("iss %v: the issuer certificate does not name %s", p.Claims["iss"], e.svc.Identifier(tax("nl")))
		}
	})
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
