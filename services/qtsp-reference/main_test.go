package main

import (
	"net/url"
	"testing"
)

func TestWalletIssuanceLink(t *testing.T) {
	link, err := walletIssuanceLink("https://issuance.example", "https://wallet.example/disclosure_based_issuance")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if got, want := query.Get("request_uri"), "https://issuance.example/disclosure/qtsp-attestation/request_uri?session_type=cross_device"; got != want {
		t.Errorf("request_uri = %q, want %q", got, want)
	}
	if got := query.Get("request_uri_method"); got != "post" {
		t.Errorf("request_uri_method = %q", got)
	}
	if got := query.Get("client_id"); got != "x509_san_dns:issuance.example" {
		t.Errorf("client_id = %q", got)
	}
}

func TestWalletIssuanceLinkRequiresPublicHTTPS(t *testing.T) {
	for _, value := range []string{"http://issuance.example", "https://issuance.example/path", "not a URL"} {
		if _, err := walletIssuanceLink(value, "https://wallet.example/issue"); err == nil {
			t.Errorf("EUDI_PUBLIC_URL %q accepted", value)
		}
	}
	for _, value := range []string{"http://wallet.example/issue", "https://user@wallet.example/issue", "https://wallet.example/issue?mode=test"} {
		if _, err := walletIssuanceLink("https://issuance.example", value); err == nil {
			t.Errorf("EUDI_UL_BASE %q accepted", value)
		}
	}
}
