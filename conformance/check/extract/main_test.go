package main

import (
	"slices"
	"testing"
)

const doc = `<section id="section-1"><h2 id="a"><a class="section-number selfRef">1. </a><a href="#x" class="section-name selfRef">Introduction</a></h2>
<p id="section-1-1">Wallets MUST be nice.<a href="#section-1-1" class="pilcrow">¶</a></p>
<section id="section-1.1"><h3 id="b"><a class="section-number selfRef">1.1. </a><a href="#y" class="section-name selfRef">Requirements Notation and Conventions</a></h3>
<p id="section-1.1-1">The key words "MUST", "SHOULD" are to be interpreted as described.<a href="#section-1.1-1" class="pilcrow">¶</a></p>
</section></section>
<section id="section-4"><h2 id="c"><a class="section-number selfRef">4. </a><a href="#z" class="section-name selfRef">Issuance</a></h2>
<p id="section-4-1">The Issuer MUST use <code>S256</code>, e.g. for PKCE. It MAY add more. The Wallet SHOULD NOT retry.<a href="#section-4-1" class="pilcrow">¶</a></p>
<ul><li id="section-4-2.1">MUST support DPoP as in Section 4.1 of [X].<a href="#section-4-2.1" class="pilcrow">¶</a>
<ul><li id="section-4-2.1.1">REQUIRED: the &quot;nonce&quot;.<a href="#section-4-2.1.1" class="pilcrow">¶</a></li></ul></li></ul>
</section>
<section id="appendix-A.2"><h3 id="d"><a class="section-number selfRef">A.2. </a><a href="#w" class="section-name selfRef">mdoc</a></h3>
<p id="appendix-A.2-1">The doctype MUST be given.<a href="#appendix-A.2-1" class="pilcrow">¶</a></p></section>`

// The statements: by sentence, per section in document order, without MAY,
// without the notation section, with nested list items on their own, and
// abbreviations and section numbers kept whole.
func TestExtract(t *testing.T) {
	var ids, texts []string
	for _, s := range Extract(doc, "X") {
		ids = append(ids, s.ID)
		texts = append(texts, s.Text)
	}
	wantIDs := []string{"X-1-01", "X-4-01", "X-4-02", "X-4-03", "X-4-04", "X-A.2-01"}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("ids = %v, want %v", ids, wantIDs)
	}
	wantTexts := []string{
		"Wallets MUST be nice.",
		"The Issuer MUST use S256, e.g. for PKCE.",
		"The Wallet SHOULD NOT retry.",
		"MUST support DPoP as in Section 4.1 of [X].",
		`REQUIRED: the "nonce".`,
		"The doctype MUST be given.",
	}
	if !slices.Equal(texts, wantTexts) {
		t.Fatalf("texts = %q", texts)
	}
}
