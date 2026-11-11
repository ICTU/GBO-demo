package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gbo-demo/oid4vc-kit/lote"
)

func defaultPIDTrustListURL() string {
	if value := os.Getenv("GBO_PID_TRUST_LIST_URL"); value != "" {
		return value
	}
	if value := os.Getenv("EUDI_TRUST_PUBLIC_URL"); value != "" {
		return strings.TrimRight(value, "/") + "/lote/PIDProviders.jwt"
	}
	return ""
}

// copyPIDTrustList copies only the independently managed public signing
// root to the runtime config. It never fetches or snapshots the PID anchors.
func copyPIDTrustList(endpoint, rootFile, dir string) (*lote.File, error) {
	if endpoint == "" {
		return nil, nil
	}
	if rootFile == "" {
		return nil, errors.New("PID LoTE URL requires an independently provisioned signer root")
	}
	file := lote.File{URL: endpoint, SignerRootFile: rootFile}
	if _, err := file.Open(func(p string) string { return p }); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(rootFile)
	if err != nil {
		return nil, err
	}
	const name = "lote-signer-root.pem"
	if err = writeFileAtomically(dir, name, raw, 0o644); err != nil {
		return nil, err
	}
	return &lote.File{URL: endpoint, SignerRootFile: name}, nil
}

// export-pid-trust is the bridge for the upstream NL verifier, which only
// loads static configuration at startup. No legacy roots are added.
func runExportPIDTrust(ctx context.Context, args []string, stderr io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "export-pid-trust" {
		return false, nil
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("url", defaultPIDTrustListURL(), "central PID LoTE HTTPS URL")
	root := fs.String("signer-root", os.Getenv("GBO_LOTE_SIGNER_ROOT_FILE"), "independently provisioned public LoTE signing CA PEM")
	out := fs.String("out-file", "", "base64 DER PID anchors for the startup-only NL verifier")
	if err := fs.Parse(args[1:]); err != nil {
		return true, err
	}
	if *out == "" || fs.NArg() != 0 {
		return true, errors.New("--out-file is required; no positional arguments")
	}
	c, err := (lote.File{URL: *endpoint, SignerRootFile: *root}).Open(func(p string) string { return p })
	if err != nil {
		return true, err
	}
	certs, err := c.Certificates(ctx)
	if err != nil {
		return true, err
	}
	var b strings.Builder
	b.WriteString("# Verified central PID LoTE snapshot; re-provision and restart to refresh.\n")
	for _, cert := range certs {
		fmt.Fprintln(&b, base64.StdEncoding.EncodeToString(cert.Raw))
	}
	if err = os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		return true, err
	}
	return true, writeFileAtomically(filepath.Dir(*out), filepath.Base(*out), []byte(b.String()), 0o644)
}
