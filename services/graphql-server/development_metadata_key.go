package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const developmentMetadataKeyWarning = "GBO DEMO ONLY - deterministic local source metadata key - never use outside local development - "

func runDevelopmentMetadataKeyCommand(arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "init-development-metadata-key" {
		return false, nil
	}
	set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	var outputPath, sourceOIN string
	set.StringVar(&outputPath, "output", "", "private JWK output path below .local")
	set.StringVar(&sourceOIN, "source-oin", "", "20-digit development source OIN")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if outputPath == "" || sourceOIN == "" {
		return true, fmt.Errorf("--output and --source-oin are required")
	}
	if len(sourceOIN) != 20 || strings.Trim(sourceOIN, "0123456789") != "" {
		return true, fmt.Errorf("--source-oin must contain exactly 20 digits")
	}
	absolute, err := filepath.Abs(outputPath)
	if err != nil {
		return true, fmt.Errorf("resolve development metadata key path: %w", err)
	}
	if !pathContainsComponent(absolute, ".local") {
		return true, fmt.Errorf("development metadata key must be written below a .local directory")
	}
	seed := sha256.Sum256([]byte(developmentMetadataKeyWarning + sourceOIN))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	jwk := sourceMetadataPrivateJWK{
		KTY: "OKP",
		CRV: "Ed25519",
		X:   sourceMetadataBase64URL.EncodeToString(publicKey),
		D:   sourceMetadataBase64URL.EncodeToString(seed[:]),
	}
	body, err := json.MarshalIndent(jwk, "", "  ")
	if err != nil {
		return true, fmt.Errorf("marshal development metadata signing JWK: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return true, fmt.Errorf("create development metadata key directory: %w", err)
	}
	if existing, err := os.ReadFile(absolute); err == nil {
		if string(existing) != string(body) {
			return true, fmt.Errorf("existing development metadata key contains different bytes")
		}
	} else if !os.IsNotExist(err) {
		return true, fmt.Errorf("read development metadata key: %w", err)
	} else if err := os.WriteFile(absolute, body, 0o600); err != nil {
		return true, fmt.Errorf("write development metadata key: %w", err)
	}
	thumbprintInput := fmt.Sprintf(`{"crv":"Ed25519","kty":"OKP","x":"%s"}`, jwk.X)
	thumbprint := sha256.Sum256([]byte(thumbprintInput))
	_, _ = fmt.Fprintf(stdout, "sha256-%s\n", sourceMetadataBase64URL.EncodeToString(thumbprint[:]))
	return true, nil
}

func pathContainsComponent(path, component string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == component {
			return true
		}
	}
	return false
}
