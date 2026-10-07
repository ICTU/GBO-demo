// Command asi-provider is a mock Authentic Source Interface Provider (ASIP)
// after ETSI TS 119 478 V1.1.1 clause 6.1: a QTSP verifies whether an
// attribute it holds matches the authentic source.
//
// Slice 1 covers the contract layer: /verify and /retrieve against an
// in-memory source, every result sealed. The authorization server is a stub:
// fixed test tokens identify test persons. It is not conformant.
//
// Usage:
//
//	asi-provider                              serve (configuration from the environment)
//	asi-provider provision-seal-certificate   issue a development seal certificate
package main

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "provision-seal-certificate" {
		if err := provisionSealCertificate(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := serve(); err != nil {
		log.Fatal(err)
	}
}

func serve() error {
	source, err := loadMemorySource(env("ASIP_SOURCE_FILE", "testdata/persons.json"))
	if err != nil {
		return err
	}
	key, chain, err := loadSealMaterial(os.Getenv("ASIP_SEAL_KEY_FILE"), os.Getenv("ASIP_SEAL_CERT_FILE"))
	if err != nil {
		return err
	}
	tokens, err := parseStubTokens(env("ASIP_STUB_TOKENS",
		"test-token-frouke:999991772,test-token-joost:123456789,test-token-sanne:987654321,test-token-tom:555555555"))
	if err != nil {
		return err
	}
	handler := newServer(serverConfig{
		Source:          source,
		Catalogue:       defaultCatalogue(),
		Sealer:          newSealer(key, chain),
		Tokens:          tokens,
		Provider:        provider{LegalName: env("ASIP_PROVIDER_NAME", "GBO demo ASIP")},
		AuthenticSource: provider{LegalName: env("ASIP_AUTHENTIC_SOURCE_NAME", "Basisregistratie Personen (mock)")},
	})
	addr := env("ASIP_ADDR", ":4020")
	log.Printf("asi-provider listening on %s (stub tokens: not a conformant authorization server)", addr)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// parseStubTokens reads "token:bsn,token:bsn".
func parseStubTokens(spec string) (map[string]string, error) {
	tokens := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		token, bsn, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok || token == "" || bsn == "" {
			return nil, fmt.Errorf("ASIP_STUB_TOKENS: %q is not token:bsn", pair)
		}
		tokens[token] = bsn
	}
	return tokens, nil
}

// loadSealMaterial reads the seal key and the certificate chain (leaf first)
// from PEM files.
func loadSealMaterial(keyPath, certPath string) (crypto.Signer, []*x509.Certificate, error) {
	if keyPath == "" || certPath == "" {
		return nil, nil, errors.New("ASIP_SEAL_KEY_FILE and ASIP_SEAL_CERT_FILE are required; run `make provision-asip-seal-certificate`")
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read seal key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, errors.New("seal key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if k, ecErr := x509.ParseECPrivateKey(block.Bytes); ecErr == nil {
			parsed = k
		} else {
			return nil, nil, fmt.Errorf("parse seal key: %w", err)
		}
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("seal key cannot sign")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read seal certificate: %w", err)
	}
	var chain []*x509.Certificate
	for rest := certPEM; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse seal certificate: %w", err)
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 {
		return nil, nil, errors.New("seal certificate file contains no certificate")
	}
	return signer, chain, nil
}
