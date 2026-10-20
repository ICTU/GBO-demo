// Command qtsp-reference is the reference QTSP of the ASI route (ETSI TS
// 119 478): it discovers an attribute's data service, registers with the
// data service's authorization server, lets the user authorize, takes a
// DPoP-bound token, verifies or retrieves, and accepts a result only with a
// valid seal. The web side is where a user starts, after logging in at the
// QTSP with the NL Wallet or test data (a mock); the CLI runs the same
// flow with the DigiD mock login played by a test harness.
//
// This file is the composition root.
//
// Usage:
//
//	qtsp-reference                        serve the web side (configuration from the environment)
//	qtsp-reference verify --attr id=json  one verify request (--help for the test-harness options)
//	qtsp-reference retrieve --attr id     one retrieve request
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gbo-demo/qtsp-reference/internal/cli"
	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/issuancecallback"
	loginport "gbo-demo/qtsp-reference/internal/login"
	"gbo-demo/qtsp-reference/internal/qtsp"
	"gbo-demo/qtsp-reference/internal/urlmap"
	"gbo-demo/qtsp-reference/internal/walletlogin"
	"gbo-demo/qtsp-reference/internal/web"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

var errIssuanceUnavailable = errors.New("QTSP issuance callback material is unavailable")

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", "qtsp-reference"))
	if len(os.Args) > 1 && (os.Args[1] == "verify" || os.Args[1] == "retrieve") {
		os.Exit(runCLI(os.Args[1], os.Args[2:]))
	}
	if err := serve(); err != nil {
		slog.Error("serving qtsp-reference", "err", err)
		os.Exit(1)
	}
}

func runCLI(operation string, args []string) int {
	opts, err := cli.Parse(operation, args, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	client, httpClient, err := newClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	outcome, err := cli.Run(ctx, client, httpClient, opts)
	return cli.Print(os.Stdout, outcome, err)
}

func serve() error {
	client, _, err := newClient()
	if err != nil {
		return err
	}
	wallet, err := walletLogin()
	if err != nil {
		return err
	}
	store := issuance.NewStore(time.Now)
	link, err := walletIssuanceLink(os.Getenv("EUDI_PUBLIC_URL"), env("EUDI_UL_BASE", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance"))
	if err != nil {
		return err
	}
	issuanceSrv, link, err := optionalIssuanceServer(store, link)
	if err != nil {
		return err
	}
	handler := web.NewHandlerWithIssuance(client, web.DemoAttributes, env("QTSP_PUBLIC_URL", "http://localhost:4023"), wallet, store, link)
	srv := &http.Server{Addr: env("QTSP_ADDR", ":4023"), Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 2)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "public_url", os.Getenv("QTSP_PUBLIC_URL"))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	if issuanceSrv != nil {
		go func() {
			slog.Info("listening for issuance callbacks", "addr", issuanceSrv.Addr)
			if err := issuanceSrv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	go func() {
		if err := registerAtStart(ctx, client); err != nil {
			errc <- err
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if issuanceSrv != nil {
		if err := issuanceSrv.Shutdown(drainCtx); err != nil {
			return err
		}
	}
	return srv.Shutdown(drainCtx)
}

func optionalIssuanceServer(store *issuance.Store, link string) (*http.Server, string, error) {
	if link == "" {
		return nil, "", nil
	}
	server, err := newIssuanceServer(store)
	if errors.Is(err, errIssuanceUnavailable) || (err == nil && server == nil) {
		slog.Warn("wallet issuance disabled because callback certificate material is absent")
		return nil, "", nil
	}
	return server, link, err
}

func walletIssuanceLink(publicURL, universalLinkBase string) (string, error) {
	if strings.TrimSpace(publicURL) == "" {
		return "", nil
	}
	issuer, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.Hostname() == "" || issuer.User != nil || (issuer.Path != "" && issuer.Path != "/") || issuer.RawQuery != "" || issuer.Fragment != "" {
		return "", errors.New("EUDI_PUBLIC_URL must be a root HTTPS URL")
	}
	base, err := url.Parse(universalLinkBase)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("EUDI_UL_BASE must be an absolute HTTPS URL without credentials, query or fragment")
	}
	requestURI := strings.TrimRight(issuer.String(), "/") + "/disclosure/" + issuance.Usecase + "/request_uri?session_type=cross_device"
	query := base.Query()
	query.Set("request_uri", requestURI)
	query.Set("request_uri_method", "post")
	query.Set("client_id", "x509_san_dns:"+issuer.Hostname())
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func newIssuanceServer(store *issuance.Store) (*http.Server, error) {
	keyFile, certFile := os.Getenv("QTSP_ISSUANCE_TLS_KEY_FILE"), os.Getenv("QTSP_ISSUANCE_TLS_CERT_FILE")
	callbackPathFile := os.Getenv("QTSP_ISSUANCE_CALLBACK_PATH_FILE")
	if keyFile == "" && certFile == "" && callbackPathFile == "" {
		return nil, nil
	}
	if keyFile == "" || certFile == "" || callbackPathFile == "" {
		return nil, errors.New("QTSP_ISSUANCE_TLS_KEY_FILE, QTSP_ISSUANCE_TLS_CERT_FILE and QTSP_ISSUANCE_CALLBACK_PATH_FILE must be configured together")
	}
	certificate, err := loadBase64TLSKeyPair(keyFile, certFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %v", errIssuanceUnavailable, err)
		}
		return nil, fmt.Errorf("load issuance callback TLS certificate: %w", err)
	}
	tokenBody, err := os.ReadFile(callbackPathFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %v", errIssuanceUnavailable, err)
		}
		return nil, fmt.Errorf("load issuance callback path: %w", err)
	}
	callbackToken, err := parseCallbackToken(string(tokenBody))
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr: env("QTSP_ISSUANCE_ADDR", ":4443"), Handler: issuancecallback.Handler(store, callbackToken), ReadHeaderTimeout: readHeaderTimeout,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
	}, nil
}

func parseCallbackToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 24 {
		return "", errors.New("QTSP issuance callback path must contain one 24-byte base64url token")
	}
	return token, nil
}

func loadBase64TLSKeyPair(keyFile, certFile string) (tls.Certificate, error) {
	read := func(path string) ([]byte, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("%s is not base64 DER: %w", path, err)
		}
		return decoded, nil
	}
	keyDER, err := read(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	certDER, err := read(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return tls.Certificate{}, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return tls.Certificate{}, errors.New("issuance callback TLS key is not ECDSA")
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return tls.Certificate{}, errors.New("issuance callback TLS key does not match its certificate")
	}
	return tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key, Leaf: cert}, nil
}

// registerAtStart registers with the authorization server of the demo's data
// service, as a QTSP does when it starts to use an ASIP. While discovery or
// the server is unavailable it waits and tries again; a refusal stops the
// QTSP with the reason.
func registerAtStart(ctx context.Context, client *qtsp.Client) error {
	wait := time.Second
	for {
		plan, err := client.Plan(ctx, []string{web.DemoAttributes[0].Identifier})
		if err == nil {
			_, err = cli.Register(ctx, client, plan.Server, time.Minute)
			if err == nil {
				slog.Info("registered", "authorization_server", plan.Server.Issuer)
				return nil
			}
			var refused *qtsp.RegistrationError
			if errors.As(err, &refused) {
				return fmt.Errorf("the authorization server refused the registration: %w", err)
			}
		}
		slog.Warn("not registered yet", "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

// newClient builds the client from the environment, and the HTTP client
// that carries it (with the URL map) for the CLI's test harness.
func newClient() (*qtsp.Client, *http.Client, error) {
	key, chain, err := loadKeyAndChain(os.Getenv("QTSP_KEY_FILE"), os.Getenv("QTSP_CERT_FILE"))
	if err != nil {
		return nil, nil, err
	}
	anchors, err := loadPool(os.Getenv("QTSP_SEAL_TRUST_ANCHORS"))
	if err != nil {
		return nil, nil, err
	}
	dip := os.Getenv("QTSP_DIP_URL")
	if dip == "" {
		return nil, nil, errors.New("QTSP_DIP_URL is required: the Discover Interface")
	}
	transport, err := urlmap.New(os.Getenv("QTSP_URL_MAP"), http.DefaultTransport)
	if err != nil {
		return nil, nil, err
	}
	httpClient := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	public := strings.TrimRight(env("QTSP_PUBLIC_URL", "http://localhost:4023"), "/")
	client, err := qtsp.New(qtsp.Config{
		DIP:         dip,
		HTTP:        httpClient,
		Key:         key,
		Chain:       chain,
		RegistryURI: env("QTSP_REGISTRY_URI", "https://register.gbo.example/wrp/test-qtsp"),
		RedirectURI: public + "/callback",
		SealAnchors: anchors,
	})
	return client, httpClient, err
}

func loadKeyAndChain(keyFile, certFile string) (*ecdsa.PrivateKey, []*x509.Certificate, error) {
	if keyFile == "" || certFile == "" {
		return nil, nil, errors.New("QTSP_KEY_FILE and QTSP_CERT_FILE are required: the QTSP certificate key and chain (make provision-asip-qtsp-certificate)")
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: no PEM key", keyFile)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, ok := parsed.(*ecdsa.PrivateKey)
	if err != nil || !ok {
		return nil, nil, fmt.Errorf("%s: not a PKCS#8 ECDSA key", keyFile)
	}
	certs, err := readCertificates(certFile)
	if err != nil || len(certs) == 0 {
		return nil, nil, fmt.Errorf("%s: no certificates: %v", certFile, err)
	}
	return key, certs, nil
}

func loadPool(file string) (*x509.CertPool, error) {
	if file == "" {
		return nil, errors.New("QTSP_SEAL_TRUST_ANCHORS is required: the roots seals must chain to")
	}
	certs, err := readCertificates(file)
	if err != nil || len(certs) == 0 {
		return nil, fmt.Errorf("%s: no certificates: %v", file, err)
	}
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool, nil
}

func readCertificates(file string) ([]*x509.Certificate, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			return out, nil
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// walletLogin is the NL Wallet as the citizen's login means, when the
// verifier is configured: QTSP_WALLET_VERIFIER_URL (its internal API),
// QTSP_WALLET_VERIFIER_STATUS_URL (its wallet API, where the session status
// is) and the API key in QTSP_WALLET_VERIFIER_API_KEY_FILE. Without the key
// file only the mock login is offered.
func walletLogin() (loginport.Wallet, error) {
	internal := os.Getenv("QTSP_WALLET_VERIFIER_URL")
	if internal == "" {
		return nil, nil
	}
	keyFile := env("QTSP_WALLET_VERIFIER_API_KEY_FILE", "/wallet-verifier/api-key")
	key, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) {
		slog.Warn("NL Wallet verifier not configured: no API key; logging in with the NL Wallet is not offered", "file", keyFile)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	status := env("QTSP_WALLET_VERIFIER_STATUS_URL", internal)
	return walletlogin.New(internal, status, strings.TrimSpace(string(key))), nil
}
