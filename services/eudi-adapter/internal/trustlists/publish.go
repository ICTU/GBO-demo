// Package trustlists owns the demo's LoTE publication use case. It receives
// source material with a declared trust mode; fetching, signing and atomic
// storage are ports.
package trustlists

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

type Role string

const (
	PID    Role = "PID"
	PubEAA Role = "PubEAA"
	WRPAC  Role = "WRPAC"
	WRPRC  Role = "WRPRC"
)

var Roles = []Role{PID, PubEAA, WRPAC, WRPRC}

func (r Role) Filename() string { return string(r) + "Providers.jwt" }

// Anchor carries an X.509 trust anchor and the URLs that account for its
// inclusion. An anchor present in both sources retains both origins.
type Anchor struct {
	DER     []byte
	Origins []string
}

type NLAuthentication string

const (
	NLJWTVerified NLAuthentication = "jwt-verified"
	NLHTTPSDemo   NLAuthentication = "https-only-demo"
)

type Snapshot struct {
	GBO              map[Role][]byte
	NL               map[Role][]Anchor
	NLVersion        uint64
	NLSource         string
	NLAuthentication NLAuthentication
}

type Source interface {
	Load(context.Context) (Snapshot, error)
}
type Signer interface {
	// Identity changes when the signing certificate or its chain changes.
	Identity() string
	Sign(context.Context, []byte, time.Time) ([]byte, error)
}

type Publication struct {
	Sequence         uint64
	Digest           string
	NLVersion        uint64
	NLSource         string
	NLAuthentication NLAuthentication
	Issue            time.Time
	NextUpdate       time.Time
	Files            map[string][]byte
}

// Publications serializes the read/replace operation, so concurrent runs
// cannot reuse a sequence number. A nil replacement leaves every byte intact.
type Publications interface {
	Update(context.Context, func(*Publication) (*Publication, error)) error
}

type Options struct {
	PublicURL string
	Validity  time.Duration
	Renew     bool
	Now       time.Time
}

type Result struct {
	Changed          bool
	Sequence         uint64
	NLVersion        uint64
	NextUpdate       time.Time
	Counts           map[Role]int
	NLAuthentication NLAuthentication
}

func Publish(ctx context.Context, options Options, source Source, signer Signer, publications Publications) (Result, error) {
	var result Result
	u, err := url.Parse(options.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return result, errors.New("trust public URL must be an absolute HTTPS URL without credentials, query or fragment")
	}
	if options.Validity <= time.Hour || options.Validity > 30*24*time.Hour || options.Now.IsZero() {
		return result, errors.New("validity must exceed the one-hour cache and be at most 30 days; publication time is required")
	}
	options.PublicURL = strings.TrimRight(options.PublicURL, "/")
	snapshot, err := source.Load(ctx)
	if err != nil {
		return result, fmt.Errorf("load trust anchors (previous publication retained): %w", err)
	}
	anchors, err := merge(snapshot, options.PublicURL)
	if err != nil {
		return result, err
	}
	content, err := json.Marshal(struct {
		Anchors               map[Role][]Anchor
		URL, Signer, NLSource string
		Version               uint64
		Validity              time.Duration
		Authentication        NLAuthentication
	}{anchors, options.PublicURL, signer.Identity(), snapshot.NLSource, snapshot.NLVersion, options.Validity, snapshot.NLAuthentication})
	if err != nil {
		return result, err
	}
	digest := Fingerprint(content)
	result.Counts = map[Role]int{}
	result.NLAuthentication = snapshot.NLAuthentication
	for role, entries := range anchors {
		result.Counts[role] = len(entries)
	}
	err = publications.Update(ctx, func(previous *Publication) (*Publication, error) {
		sequence := uint64(1)
		if previous != nil {
			if previous.NLSource == snapshot.NLSource && snapshot.NLVersion < previous.NLVersion {
				return nil, errors.New("NL configuration version decreased; previous publication retained")
			}
			result.Sequence, result.NLVersion, result.NextUpdate = previous.Sequence, previous.NLVersion, previous.NextUpdate
			renewBefore := min(24*time.Hour, options.Validity/4)
			if previous.Digest == digest && !options.Renew && options.Now.Add(renewBefore).Before(previous.NextUpdate) {
				return nil, nil
			}
			sequence = previous.Sequence + 1
			if sequence == 0 {
				return nil, errors.New("LoTE sequence number exhausted")
			}
		}
		issue := options.Now.UTC().Truncate(time.Second)
		publication := &Publication{Sequence: sequence, Digest: digest, NLVersion: snapshot.NLVersion, NLSource: snapshot.NLSource, NLAuthentication: snapshot.NLAuthentication,
			Issue: issue, NextUpdate: issue.Add(options.Validity), Files: map[string][]byte{}}
		for _, role := range Roles {
			body, err := json.Marshal(document(role, anchors[role], options.PublicURL, publication))
			if err != nil {
				return nil, err
			}
			signed, err := signer.Sign(ctx, body, issue)
			if err != nil {
				return nil, fmt.Errorf("sign %s: %w", role, err)
			}
			publication.Files[role.Filename()] = signed
		}
		result.Changed, result.Sequence, result.NLVersion, result.NextUpdate = true, sequence, snapshot.NLVersion, publication.NextUpdate
		return publication, nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func Fingerprint(der []byte) string { sum := sha256.Sum256(der); return hex.EncodeToString(sum[:]) }

func merge(snapshot Snapshot, publicURL string) (map[Role][]Anchor, error) {
	expected := map[Role]string{PID: "GBO test-PID root", PubEAA: "ca.gbo-issuer", WRPAC: "ca.gbo-reader", WRPRC: "GBO development registrar root"}
	known := map[string]Role{}
	for role, name := range expected {
		known[name] = role
	}
	if snapshot.NLVersion == 0 || snapshot.NLSource == "" || (snapshot.NLAuthentication != NLJWTVerified && snapshot.NLAuthentication != NLHTTPSDemo) {
		return nil, errors.New("NL configuration, explicit authentication mode and provenance are required")
	}
	out := map[Role][]Anchor{}
	assigned := map[string]Role{}
	for role := range snapshot.NL {
		if !slices.Contains(Roles, role) {
			return nil, fmt.Errorf("unknown NL role %q", role)
		}
	}
	for _, role := range Roles {
		gbo, err := x509.ParseCertificate(snapshot.GBO[role])
		if err != nil || gbo.Subject.CommonName != expected[role] {
			return nil, fmt.Errorf("%s requires its own GBO root %q", role, expected[role])
		}
		entries := append([]Anchor{{DER: gbo.Raw, Origins: []string{publicURL + "/certificate-policy#GBO"}}}, snapshot.NL[role]...)
		byHash := map[string]Anchor{}
		for _, entry := range entries {
			cert, err := x509.ParseCertificate(entry.DER)
			if err != nil || !cert.IsCA || !cert.BasicConstraintsValid {
				return nil, fmt.Errorf("%s contains an invalid CA certificate", role)
			}
			if wanted, ok := known[cert.Subject.CommonName]; ok && wanted != role {
				return nil, fmt.Errorf("GBO anchor %s belongs to %s, not %s", cert.Subject.CommonName, wanted, role)
			}
			hash := Fingerprint(entry.DER)
			if earlier, ok := assigned[hash]; ok && earlier != role {
				return nil, fmt.Errorf("anchor %s occurs in both %s and %s", hash, earlier, role)
			}
			assigned[hash] = role
			if len(entry.Origins) == 0 {
				return nil, errors.New("anchor provenance is required")
			}
			old := byHash[hash]
			old.DER = entry.DER
			old.Origins = append(old.Origins, entry.Origins...)
			slices.Sort(old.Origins)
			old.Origins = slices.Compact(old.Origins)
			byHash[hash] = old
		}
		hashes := make([]string, 0, len(byHash))
		for hash := range byHash {
			hashes = append(hashes, hash)
		}
		slices.Sort(hashes)
		for _, hash := range hashes {
			out[role] = append(out[role], byHash[hash])
		}
	}
	return out, nil
}

type object = map[string]any

func label(value string) []object { return []object{{"lang": "en", "value": value}} }
func uri(value string) object     { return object{"lang": "en", "uriValue": value} }
func address(prefix, base string) object {
	return object{prefix + "PostalAddress": []object{{"lang": "en", "StreetAddress": "Demo fixture - no postal address", "Locality": "Simulation", "Country": "NL"}},
		prefix + "ElectronicAddress": []object{uri(base + "/support")}}
}

// The ETSI role identifiers allow the demo wallet's existing LoTE parser to
// select a service. They do not assert EU notification or legal approval.
func document(role Role, anchors []Anchor, base string, p *Publication) object {
	location := base + "/lote/" + role.Filename()
	notice := "Demo trust only; no EU notification or legal approval is asserted."
	if p.NLAuthentication == NLHTTPSDemo {
		notice += " Demo shortcut: NL anchors are trusted through the official HTTPS source; the NL configuration JWT signature was not verified."
	}
	info := object{
		"LoTEVersionIdentifier": 1, "LoTESequenceNumber": p.Sequence,
		"LoTEType":           "http://uri.etsi.org/19602/LoTEType/EU" + string(role) + "ProvidersList",
		"SchemeOperatorName": label("GBO Demo"), "SchemeOperatorAddress": address("SchemeOperator", base),
		"SchemeName":                  label("GBO Demo " + string(role) + " providers (simulation only)"),
		"SchemeInformationURI":        []object{uri(base + "/certificate-policy")},
		"StatusDeterminationApproach": base + "/certificate-policy#demo-trust",
		"SchemeTypeCommunityRules":    []object{uri(base + "/certificate-policy")},
		"SchemeTerritory":             "NL", "PolicyOrLegalNotice": []object{{"LoTELegalNotice": notice}},
		"ListIssueDateTime": p.Issue.Format(time.RFC3339), "NextUpdate": p.NextUpdate.Format(time.RFC3339),
		"DistributionPoints": []string{location},
	}
	entities := make([]object, 0, len(anchors))
	for _, anchor := range anchors {
		cert, _ := x509.ParseCertificate(anchor.DER) // merge already checked every certificate.
		origins := make([]object, 0, len(anchor.Origins))
		for _, origin := range anchor.Origins {
			origins = append(origins, uri(origin))
		}
		services := []object{}
		for _, purpose := range []string{"Issuance", "Revocation"} {
			services = append(services, object{"ServiceInformation": object{
				"ServiceTypeIdentifier":  "http://uri.etsi.org/19602/SvcType/" + string(role) + "/" + purpose,
				"ServiceName":            label(cert.Subject.CommonName + " " + purpose),
				"ServiceDigitalIdentity": object{"X509Certificates": []object{{"val": base64.StdEncoding.EncodeToString(anchor.DER)}}},
			}})
		}
		entities = append(entities, object{"TrustedEntityInformation": object{"TEName": label(cert.Subject.CommonName),
			"TEAddress": address("TE", base), "TEInformationURI": origins}, "TrustedEntityServices": services})
	}
	return object{"LoTE": object{"ListAndSchemeInformation": info, "TrustedEntitiesList": entities}}
}
