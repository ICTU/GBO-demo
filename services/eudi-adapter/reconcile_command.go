package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type reconcileOptions struct {
	managerURL       string
	managerCAPath    string
	managerCertPath  string
	managerKeyPath   string
	consumerOIN      string
	outwayURL        string
	schemaPath       string
	publicBaseURL    string
	storageBackend   string
	certificateStore string
	stateDir         string
	secretsDir       string
	readerPublicURL  string
	readerOrigin     string
	watch            bool
	interval         time.Duration
}

type reconcileDependencies struct {
	sourceClient     *http.Client
	now              func() time.Time
	newManagerClient func(string, string, string) (*http.Client, error)
	stdout           io.Writer
	stderr           io.Writer
}

func defaultReconcileDependencies() reconcileDependencies {
	return reconcileDependencies{
		sourceClient:     &http.Client{Timeout: 15 * time.Second},
		now:              time.Now,
		newManagerClient: newFSCManagerHTTPClient,
		stdout:           os.Stdout,
		stderr:           os.Stderr,
	}
}

func runReconcileCommand(ctx context.Context, arguments []string, dependencies reconcileDependencies) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "reconcile-fsc-sources" {
		return false, nil
	}
	options, err := parseReconcileOptions(arguments[1:], dependencies.stderr)
	if err != nil {
		return true, err
	}
	managerClient, err := dependencies.newManagerClient(options.managerCAPath, options.managerCertPath, options.managerKeyPath)
	if err != nil {
		return true, err
	}
	onboarding := onboardingOptions{
		storageBackend: options.storageBackend, certificateStoreName: options.certificateStore,
		stateDir: options.stateDir, secretsDir: options.secretsDir,
		readerPublicURL: options.readerPublicURL, readerOrigin: options.readerOrigin,
	}
	store, err := configuredCertificateStore(onboarding)
	if err != nil {
		return true, err
	}
	backend, err := configuredActivationBackend(onboarding)
	if err != nil {
		return true, err
	}
	reconciler := &fscSourceReconciler{
		managerClient: managerClient, sourceClient: dependencies.sourceClient,
		managerURL: options.managerURL, consumerOIN: options.consumerOIN, outwayURL: options.outwayURL,
		schemaPath: options.schemaPath, publicBaseURL: options.publicBaseURL,
		store: store, backend: backend,
	}
	reconcile := func() error {
		if err := reconciler.Reconcile(ctx, dependencies.now()); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(dependencies.stdout, "FSC source reconciliation completed")
		return nil
	}
	if !options.watch {
		return true, reconcile()
	}
	if err := reconcile(); err != nil {
		slog.Error("initial FSC source reconciliation failed", "err", err.Error())
	}
	ticker := time.NewTicker(options.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-ticker.C:
			if err := reconcile(); err != nil {
				slog.Error("FSC source reconciliation failed", "err", err.Error())
			}
		}
	}
}

func parseReconcileOptions(arguments []string, errorOutput io.Writer) (reconcileOptions, error) {
	set := flag.NewFlagSet("reconcile-fsc-sources", flag.ContinueOnError)
	set.SetOutput(errorOutput)
	options := reconcileOptions{}
	set.StringVar(&options.managerURL, "manager-url", os.Getenv("FSC_MANAGER_URL"), "FSC consumer Manager internal base URL")
	set.StringVar(&options.managerCAPath, "manager-ca", os.Getenv("FSC_MANAGER_CA_FILE"), "FSC Manager TLS CA file")
	set.StringVar(&options.managerCertPath, "manager-cert", os.Getenv("FSC_MANAGER_CERT_FILE"), "FSC Manager mTLS client certificate")
	set.StringVar(&options.managerKeyPath, "manager-key", os.Getenv("FSC_MANAGER_KEY_FILE"), "FSC Manager mTLS client key")
	set.StringVar(&options.consumerOIN, "consumer-oin", getEnv("ISSUER_OIN", "99999999900000000100"), "OIN of the FSC consumer peer")
	set.StringVar(&options.outwayURL, "outway-url", getEnv("FSC_OUTWAY_URL", "http://localhost:8087"), "FSC Outway base URL")
	set.StringVar(&options.schemaPath, "schema", "schemas/gbo-source-metadata-v1.schema.json", "source metadata JSON Schema")
	set.StringVar(&options.publicBaseURL, "type-metadata-base-url", os.Getenv("TYPE_METADATA_PUBLIC_BASE_URL"), "public Type Metadata base URL")
	set.StringVar(&options.storageBackend, "storage-backend", getEnv("ONBOARDING_STORAGE_BACKEND", "filesystem"), "onboarding state backend")
	set.StringVar(&options.certificateStore, "certificate-store", getEnv("ONBOARDING_CERTIFICATE_STORE", "filesystem"), "store containing manually provisioned certificates")
	set.StringVar(&options.stateDir, "state-dir", ".local/onboarding", "filesystem onboarding state directory")
	set.StringVar(&options.secretsDir, "secrets-dir", ".local/secrets", "filesystem secret directory")
	set.StringVar(&options.readerPublicURL, "reader-public-url", os.Getenv("EUDI_PUBLIC_URL"), "public issuance-server URL")
	set.StringVar(&options.readerOrigin, "reader-origin-url", os.Getenv("EUDI_READER_ORIGIN_URL"), "public reader origin")
	set.BoolVar(&options.watch, "watch", false, "continuously reconcile FSC contracts")
	set.DurationVar(&options.interval, "interval", 30*time.Second, "poll interval in watch mode")
	if err := set.Parse(arguments); err != nil {
		return reconcileOptions{}, err
	}
	if set.NArg() != 0 {
		return reconcileOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	for name, value := range map[string]string{
		"--manager-url": options.managerURL, "--manager-ca": options.managerCAPath,
		"--manager-cert": options.managerCertPath, "--manager-key": options.managerKeyPath,
		"--consumer-oin": options.consumerOIN, "--outway-url": options.outwayURL,
		"--schema": options.schemaPath, "--type-metadata-base-url": options.publicBaseURL,
	} {
		if strings.TrimSpace(value) == "" {
			return reconcileOptions{}, fmt.Errorf("%s is required", name)
		}
	}
	if !sourceOINPattern.MatchString(options.consumerOIN) {
		return reconcileOptions{}, fmt.Errorf("--consumer-oin must contain exactly 20 digits")
	}
	if options.interval <= 0 {
		return reconcileOptions{}, fmt.Errorf("--interval must be positive")
	}
	return options, nil
}
