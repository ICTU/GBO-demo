// Package decryption talks to the source's own decryption component. It is a
// driven adapter for substitution.Decrypter.
//
// The component reads a value BSNk made for this source, with the source's
// keys. It keeps no keys itself: every call carries them. This adapter
// therefore holds the keys, read once from the directory the source keeps them
// in, and hands them over with each value.
package decryption

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"gbo-demo/bron-sidecar/substitution"
)

// schemeKeysFile holds BSNk's public scheme keys, by URN, in the key
// directory. A value is signed with one of them.
const schemeKeysFile = "scheme-keys.json"

// Component is the decryption component, called with this source's keys.
type Component struct {
	URL    string
	Client *http.Client
	// Keys are the source's key files, in PEM: one per kind of key and per
	// version of the source's keys.
	Keys []string
	// SchemeKeys are BSNk's public scheme keys, by URN.
	SchemeKeys map[string]string
}

// LoadKeys reads the source's key files (*.pem) and the scheme keys from dir.
// A source keeps the keys of an earlier version for as long as consent tokens
// made for it are valid, so the directory may hold more than one version.
func LoadKeys(dir string) (keys []string, schemeKeys map[string]string, err error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.pem"))
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(files)
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, err
		}
		keys = append(keys, string(content))
	}
	if len(keys) == 0 {
		return nil, nil, fmt.Errorf("no key files (*.pem) in %s", dir)
	}
	content, err := os.ReadFile(filepath.Join(dir, schemeKeysFile))
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(content, &schemeKeys); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", schemeKeysFile, err)
	}
	return keys, schemeKeys, nil
}

// Identity has the component read an encrypted identity. The component checks
// BSNk's signature and that the source holds a key for the party and key set
// version the value was made for; a failure comes back as plain text.
func (c Component) Identity(ctx context.Context, value string) (substitution.Identity, error) {
	var out struct {
		BSN          string `json:"bsn"`
		DecodedInput struct {
			SignedEI struct {
				EncryptedIdentity struct {
					Recipient string `json:"recipient"`
				} `json:"encryptedIdentity"`
			} `json:"signedEI"`
		} `json:"decodedInput"`
	}
	if err := c.read(ctx, "/signed-encrypted-identity", "signedEncryptedIdentity", value, &out); err != nil {
		return substitution.Identity{}, err
	}
	return substitution.Identity{BSN: out.BSN, Recipient: out.DecodedInput.SignedEI.EncryptedIdentity.Recipient}, nil
}

// Pseudonym has the component read an encrypted pseudonym, with the same
// checks as for an identity. What comes back is the source's own pseudonym of
// the citizen in the form the source stores it.
func (c Component) Pseudonym(ctx context.Context, value string) (substitution.Pseudonym, error) {
	var out struct {
		Pseudonym        string `json:"pseudonym"`
		DecodedPseudonym struct {
			Recipient string `json:"recipient"`
		} `json:"decodedPseudonym"`
	}
	if err := c.read(ctx, "/signed-encrypted-pseudonym", "signedEncryptedPseudonym", value, &out); err != nil {
		return substitution.Pseudonym{}, err
	}
	return substitution.Pseudonym{Value: out.Pseudonym, Recipient: out.DecodedPseudonym.Recipient}, nil
}

// read hands the component one value with this source's keys, at path and
// under field, and decodes its answer into out.
func (c Component) read(ctx context.Context, path, field, value string, out any) error {
	if len(c.Keys) == 0 {
		return errors.New("this source has no decryption keys configured")
	}
	body, err := json.Marshal(map[string]any{
		field:                 value,
		"serviceProviderKeys": c.Keys,
		"schemeKeys":          c.SchemeKeys,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("decryption component answered %d: %s", resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("decryption component answer: %w", err)
	}
	return nil
}
