// GBO-demo AppHost. Run it from this directory with `aspire run`; see
// README.md for the profiles and settings, and PLAN.md for the migration.
//
// The Go services run as containers built from their own Dockerfiles, with
// the same hostnames, container ports and mounts they had under compose. The
// FSC seeds, the PIP pull config, the FSC Inways and the collector address
// them by name, and Aspire's container network resolves resource names the
// same way compose did.
//
// Workarounds for gaps in the Go AppHost preview are marked
// `TODO(go-apphost/<gap>)`. GO-APPHOST.md lists each gap with its upstream
// issue, so the workaround can go when Aspire catches up.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"apphost/modules/aspire"
)

func main() {
	builder, err := aspire.CreateBuilder()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	m := newModel(builder)
	m.build()

	app, err := builder.Build()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	if err := app.Run(); err != nil {
		log.Fatal(aspire.FormatError(err))
	}
}

// build declares the whole app model, in the order of the C# AppHost it
// replaces.
func (m *model) build() {
	b := m.b

	// `aspire publish` writes a docker-compose.yaml for running the same model
	// without Aspire. It is generated output (aspire-output/), never edited.
	b.AddDockerComposeEnvironment("compose")

	// ── Profiles ─────────────────────────────────────────────────────────
	// Which parts of the demo run, as the compose profiles did. Set them with
	// Gbo__Profiles, comma separated (Gbo__Profiles=full aspire run). Without
	// any, the dvtp demo runs, as `make demo` did; `core` runs the core only.
	//   core        sources, PDP, logbooks, developer portal and landing page
	//   dvtp        consent flow over FSC: consumer, consent portal, IR, LVG
	//   eudi        EUDI wallet issuance over FSC (needs public URLs and CAs)
	//   full        dvtp + eudi
	//   onboarding  source registry and reconciler only
	//   integrator  the integrator peer and backend (with dvtp or full)
	//   asi         the ASI route: ASIP, authorization server, QTSP (with eudi or full)
	//   manager     the OpenFTV Manager owning policy distribution
	//   credential-issuer   the own credential issuer, trust host and test PID source
	// One-off host provisioning (wallet registration, trust lists, test PID,
	// wallet verifiers) is aspire/scripts/provision.sh.
	profileList := m.config("Gbo:Profiles")
	if strings.TrimSpace(profileList) == "" {
		profileList = "dvtp"
	}
	profiles := map[string]bool{}
	for _, p := range strings.Split(profileList, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			profiles[p] = true
		}
	}
	if profiles["full"] {
		profiles["dvtp"], profiles["eudi"] = true, true
	}
	if profiles["eudi"] || profiles["credential-issuer"] || profiles["asi"] {
		profiles["onboarding"] = true
	}

	// Host-only material lives outside the repository's tracked files: the
	// pre-provisioned development CAs and every leaf signed from them stay in
	// .local/secrets and are signed on the host with go run, so the CA keys
	// never enter a container.
	secretsDir := m.configOr("Gbo:SecretsDir", m.repo(".local/secrets"))
	publicCertificatesDir := m.configOr("Gbo:PublicCertificatesDir", m.repo(".local/public-certificates"))
	developmentCaDir := filepath.Join(secretsDir, "development-ca")
	trustPublishDir := m.configOr("Gbo:TrustPublishDir", m.repo(".local/public-trust"))

	// The EUDI parts need material a clone does not have: the development CAs
	// the test wallet trusts (never generated, see README), and for the own
	// credential issuer a wallet registration. Without it the AppHost leaves
	// those parts out and starts everything else, and the eudi-not-configured
	// step in the dashboard says what is missing and how to add it.
	var eudiNotices []string
	var missingCas []string
	for _, f := range []string{"issuer-ca-key.pem", "issuer-ca-cert.pem", "reader-ca-key.pem", "reader-ca-cert.pem"} {
		if !fileExists(filepath.Join(developmentCaDir, f)) {
			missingCas = append(missingCas, f)
		}
	}
	if profiles["onboarding"] && len(missingCas) > 0 {
		var skipped []string
		for _, p := range []string{"onboarding", "eudi", "asi", "credential-issuer"} {
			if profiles[p] {
				skipped = append(skipped, p)
				delete(profiles, p)
			}
		}
		verb := "are"
		if len(missingCas) == 1 {
			verb = "is"
		}
		eudiNotices = append(eudiNotices, fmt.Sprintf(
			"Left out: %s. These need the pre-provisioned development CAs that the test wallet trusts, "+
				"and %s %s missing from %s. Put them there (or set Gbo:SecretsDir to their directory) and start the AppHost again. "+
				"They are never generated: a wallet only trusts CAs on its trust list. See \"The complete demo, with EUDI\" in README.md.",
			strings.Join(skipped, ", "), strings.Join(missingCas, ", "), verb, developmentCaDir))
	}
	if profiles["credential-issuer"] && !fileExists(filepath.Join(trustPublishDir, "reader-root.crl")) {
		eudiNotices = append(eudiNotices,
			"Left out: credential-issuer. It needs a wallet registration first: run "+
				"aspire/scripts/provision.sh development-registrar-root once, then aspire/scripts/provision.sh wallet-registration SOURCE_ID=<id> "+
				"with the eudi profile running, and start the AppHost again. See README.md.")
		delete(profiles, "credential-issuer")
	}
	on := func(p string) bool { return profiles[p] }
	fscOn := on("dvtp") || on("eudi") || on("onboarding") || on("integrator") || on("asi")

	// ── Settings ─────────────────────────────────────────────────────────
	// Browser-facing links, as GBO_PUBLIC_HOST and GBO_PUBLIC_SCHEME were.
	publicHost := m.configOr("Gbo:PublicHost", "localhost")
	publicScheme := m.configOr("Gbo:PublicScheme", "http")
	publicURL := func(port int) string { return fmt.Sprintf("%s://%s:%d", publicScheme, publicHost, port) }

	// ── Secrets ──────────────────────────────────────────────────────────
	// Database passwords are generated on first run and kept in the AppHost
	// user secrets (aspire secret list). The LDV values default to the local
	// demo values compose used; set real ones with aspire secret set.
	ftvPostgresPassword := m.generated("ftv-postgres-password")
	ftvAdlReaderPassword := m.generated("ftv-adl-reader-password")
	fscPostgresPassword := m.generated("fsc-postgres-password")
	ldvWriteToken := m.secret("ldv-write-token", "local-demo-logboek-token")
	ldvReadToken := m.secret("ldv-read-token", "local-demo-logboek-read-token")
	pseudonymKeyBd := m.secret("ldv-subject-pseudonym-key", "local-demo-pseudonym-key")
	pseudonymKeyBrp := m.secret("ldv-subject-pseudonym-key-brp", "local-demo-pseudonym-key-brp")
	pseudonymKeyGbo := m.secret("ldv-subject-pseudonym-key-gbo", "local-demo-pseudonym-key-gbo")
	pseudonymKeyLvg := m.secret("ldv-subject-pseudonym-key-lvg", "local-demo-pseudonym-key-lvg")
	subjectRefKey := m.secret("subject-ref-key", "local-demo-subject-ref-key-not-a-secret")
	bsnAuthorisedOins := b.AddParameter("bsn-authorised-oins", aspire.AddParameterValue("99999999900000000200"))

	// ── Telemetry ────────────────────────────────────────────────────────
	// The Aspire dashboard is the local observability store: traces, logs and
	// metrics, kept across restarts (Resume persistence, aspire.config.json).
	// The services export to the collector; the collector forwards to the
	// dashboard and to dev-portal-backend's live strip. The developer portal
	// reads traces and the PDP's decision log back through the dashboard's
	// telemetry API.
	//
	// Both dashboard keys are fixed here so the collector and
	// dev-portal-backend can be given them: AppHost:OtlpApiKey and
	// AppHost:DashboardApiKey from configuration (aspire secret set), or fresh
	// random ones per run.
	keys := map[string]string{}
	for _, key := range []string{"AppHost:OtlpApiKey", "AppHost:DashboardApiKey"} {
		value := m.config(key)
		if value == "" {
			value = randomHex(16)
			m.b.GetConfiguration().GetSection(key).SetValue(&value)
		}
		keys[key] = value
	}
	// Under `aspire run` the dashboard listens on the host; in a published
	// compose file it is the compose environment's dashboard container.
	dashboardOtlpGrpc := "compose-dashboard:18889"
	dashboardURL := "http://compose-dashboard:18888"
	dashboardPublicURL := "http://localhost:18888"
	if m.runMode {
		raw := m.config("ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL")
		if raw == "" {
			log.Fatal("ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL is not set; start the AppHost with `aspire run`.")
		}
		otlp, err := url.Parse(raw)
		if err != nil || otlp.Scheme != "http" {
			log.Fatal("The collector forwards to the dashboard over plaintext gRPC; keep the http profile in aspire.config.json (and no https one).")
		}
		dashboardOtlpGrpc = "host.docker.internal:" + otlp.Port()
		frontend, err := url.Parse(strings.Split(m.configOr("ASPNETCORE_URLS", "http://localhost:15126"), ";")[0])
		if err != nil {
			log.Fatalf("ASPNETCORE_URLS: %v", err)
		}
		dashboardURL = "http://host.docker.internal:" + frontend.Port()
		dashboardPublicURL = fmt.Sprintf("%s://%s:%s", publicScheme, publicHost, frontend.Port())
	}

	otelCollector := m.container("otel-collector", "otel/opentelemetry-collector-contrib", "0.96.0").
		WithBindMount(m.repo("aspire/otel-collector.yaml"), "/etc/otelcol-contrib/config.yaml", aspire.BindMountIsReadOnly(true)).
		WithEndpoint(aspire.EndpointTargetPort(4317), aspire.EndpointName("otlp-grpc"), aspire.EndpointScheme("http")).
		WithEndpoint(aspire.EndpointTargetPort(4318), aspire.EndpointName("otlp-http"), aspire.EndpointScheme("http")).
		WithHttpEndpoint(aspire.HttpEndpointTargetPort(13133), aspire.HttpEndpointName("health")).
		WithHttpHealthCheck(aspire.HttpHealthCheckPath("/"), aspire.HttpHealthCheckEndpointName("health")).
		WithContainerRuntimeArgs([]string{"--add-host", "host.docker.internal:host-gateway"}).
		WithEnvironment("ASPIRE_DASHBOARD_OTLP_GRPC", dashboardOtlpGrpc).
		WithEnvironment("ASPIRE_DASHBOARD_OTLP_API_KEY", keys["AppHost:OtlpApiKey"])

	// A service built from its Dockerfile, exporting spans to the collector.
	// hostPort 0 means a proxied endpoint on a port Aspire picks.
	goService := func(name, dockerfile string, port int, context string, hostPort int) aspire.ContainerResource {
		if context == "" {
			context = ".."
		}
		endpoint := []aspire.WithHttpEndpointOption{aspire.HttpEndpointTargetPort(port), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(hostPort == 0)}
		if hostPort != 0 {
			endpoint = append(endpoint, aspire.HttpEndpointPort(hostPort))
		}
		return m.dockerfile(name, context, dockerfile).
			WithHttpEndpoint(endpoint...).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/health")).
			WithEnvironment("OTEL_SERVICE_NAME", name).
			WithEnvironment("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4317").
			WaitFor(otelCollector)
	}

	// ── PKI tooling ──────────────────────────────────────────────────────
	// The pki-tools image (cfssl, openssl, curl, jq, psql) signs the FSC
	// certificates and runs the seeds. Built once, cached by Docker.
	pkiToolsImage := m.executable("pki-tools-image", "docker", m.repo("fsc-infra/pki"),
		"build", "-q", "-f", "Dockerfile.pki", "-t", "gbo-demo/pki-tools:local", ".")

	// The PDP's TLS certificate (SAN openftv-pdp), made once. Skipped when it exists.
	pdpCert := m.executable("pdp-cert", "bash", m.repoRoot, "fsc-infra/pki/generate-pdp-cert.sh").
		WaitForCompletion(pkiToolsImage)

	// A broken Rego module is swallowed by the PDP; this stops the start instead.
	policyCheck := m.executable("policy-check", "bash", m.repoRoot, "scripts/policy-check.sh")

	// A run-once step in the pki-tools image, on the AppHost's network, with
	// fsc-infra mounted at /work. Retried, since the FSC Managers have no
	// health check to wait for; the seed scripts are idempotent.
	pkiStep := func(name, script string, env ...[2]string) aspire.ContainerResource {
		step := m.container(name, "gbo-demo/pki-tools", "local").
			WithImagePullPolicy(aspire.ImagePullPolicyNever).
			WithBindMount(m.repo("fsc-infra"), "/work", aspire.BindMountIsReadOnly(true)).
			WithEnvironment("FSC_POSTGRES_PASSWORD", fscPostgresPassword).
			WithEntrypoint("bash").
			WithArgs([]string{"-c", fmt.Sprintf(`cd /work && for i in $(seq 1 10); do bash %s && exit 0; echo "attempt $i failed, retrying" >&2; sleep 6; done; exit 1`, script)}).
			WaitForCompletion(pkiToolsImage)
		for _, kv := range env {
			step.WithEnvironment(kv[0], kv[1])
		}
		return step
	}

	// ── Logboek Dataverwerkingen: one per Verantwoordelijke ──────────────
	logboeken := map[string]aspire.ContainerResource{}
	for _, l := range [][3]string{
		{"logboek-bd", "bd", "https://logboek.belastingdienst.nl/data-processing-operations"},
		{"logboek-brp", "brp", "https://logboek.rvig.nl/data-processing-operations"},
		{"logboek-toestemming", "toestemming", "https://logboek.gbo.overheid.nl/toestemming/data-processing-operations"},
		{"logboek-eudi-adapter", "eudi-adapter", "https://logboek.gbo.overheid.nl/eudi-adapter/data-processing-operations"},
		{"logboek-afnemer", "afnemer", "https://logboek.hypotheek-bv.test/data-processing-operations"},
		{"logboek-lvg", "lvg", "https://logboek.lvg.test/data-processing-operations"},
		{"logboek-ir", "ir", "https://logboek.installatieregister.test/data-processing-operations"},
	} {
		name, register, logbookID := l[0], l[1], l[2]
		logboeken[name] = goService(name, "services/ldv-logboek/Dockerfile", 4016, "", 0).
			WithEnvironment("DATABASE_PATH", "/data/logboek.db").
			WithEnvironment("REGISTER_PATH", fmt.Sprintf("/config/verwerkingsactiviteiten-%s.json", register)).
			WithEnvironment("LDV_LOGBOOK_ID", logbookID).
			WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
			WithEnvironment("LDV_READ_TOKEN", ldvReadToken).
			WithVolume("/data", aspire.VolumeName("gbo-"+name+"-data"))
	}

	// ── OpenFTV: decision log database and PDP ───────────────────────────
	postgresFtv := m.container("postgres-ftv", "postgres", "16-alpine").
		WithEndpoint(aspire.EndpointTargetPort(5432), aspire.EndpointName("tcp"), aspire.EndpointScheme("tcp")).
		WithEnvironment("POSTGRES_USER", "ftv").
		WithEnvironment("POSTGRES_DB", "ftv_manager").
		WithEnvironment("POSTGRES_PASSWORD", ftvPostgresPassword).
		WithBindMount(m.repo("services/openftv-manager/initdb"), "/docker-entrypoint-initdb.d", aspire.BindMountIsReadOnly(true)).
		WithVolume("/var/lib/postgresql/data", aspire.VolumeName("gbo-postgres-ftv-data"))

	// Creates the SELECT-only role the developer portal reads the ADL with.
	postgresFtvInit := m.container("postgres-ftv-init", "postgres", "16-alpine").
		WithEnvironment("PGHOST", "postgres-ftv").
		WithEnvironment("PGUSER", "ftv").
		WithEnvironment("PGPASSWORD", ftvPostgresPassword).
		WithEnvironment("PGDATABASE", "ftv_adl").
		WithEnvironment("ADL_READER_PASSWORD", ftvAdlReaderPassword).
		WithBindMount(m.repo("services/openftv-manager/adl/adl-reader.sql"), "/init/adl-reader.sql", aspire.BindMountIsReadOnly(true)).
		WithEntrypoint("sh").
		WithArgs([]string{"-c", strings.TrimSpace(`
for i in $(seq 1 60); do psql -c 'select 1' >/dev/null 2>&1 && break; sleep 1; done
psql -v ON_ERROR_STOP=1 -v reader_password="$ADL_READER_PASSWORD" -f /init/adl-reader.sql
`)}).
		WaitFor(postgresFtv)

	dvtpOnboardingRegister := m.dockerfile("dvtp-onboarding-register", "../services/dvtp-onboarding-register", "").
		WithHttpEndpoint(aspire.HttpEndpointTargetPort(4015), aspire.HttpEndpointName("http")).
		WithHttpHealthCheck(aspire.HttpHealthCheckPath("/health")).
		WithEnvironment("DATABASE_PATH", "/data/onboarding.db").
		WithEnvironment("SEED_DEMO_DATA", "true").
		WithVolume("/data", aspire.VolumeName("gbo-dvtp-onboarding-data"))

	consentRegister := goService("consent-register", "services/consent-register/Dockerfile", 4002, "", 0).
		WithEnvironment("CONSENT_STORE", "memory").
		WithEnvironment("CONSENT_TOKEN_ISSUER", "https://consent-register.gbo.test").
		WithEnvironment("CONSENT_TOKEN_AUDIENCE", "gbo:dvtp:pdp").
		WithEnvironment("STATUS_PORT", "4012").
		WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-toestemming:4016").
		WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
		WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyGbo).
		WaitFor(logboeken["logboek-toestemming"])

	pdpBundleManager := ""
	if on("manager") {
		pdpBundleManager = "http://openftv-manager:9443/v1/bundle/gbo-pdp"
	}
	openftvPdp := m.dockerfile("openftv-pdp", "..", "services/openftv-pdp/Dockerfile").
		WithHttpsEndpoint(aspire.HttpsEndpointTargetPort(8443), aspire.HttpsEndpointName("https")).
		WithHttpEndpoint(aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("health")).
		WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz"), aspire.HttpHealthCheckEndpointName("health")).
		WithEnvironment("PDP_POLICIES_LANGUAGE", "rego").
		WithEnvironment("PDP_POLICIES_STORE", "/policies").
		WithEnvironment("PDP_POLICIES_STORE_RECURSE", "true").
		WithEnvironment("PDP_ADL_PG_URL", aspire.RefExpr("postgres://ftv:%v@postgres-ftv:5432/ftv_adl?sslmode=disable", ftvPostgresPassword)).
		WithEnvironment("PDP_ADL_SERVICE_NAME", "gbo").
		WithEnvironment("PDP_LOG_LEVEL", "info").
		WithEnvironment("PDP_REQUEST_MAPPINGS", "ftvgraphql").
		WithEnvironment("PDP_PIP_PULL_CONFIGS", "/pip-pull/dvtp-onboarding.yaml").
		WithEnvironment("GBO_CONSENT_URL", "http://consent-register:4002").
		WithEnvironment("GBO_CONSENT_STATUS_URL", "http://pdp-outway:8080/consent-status").
		WithEnvironment("GBO_CONSENT_ISSUER", "https://consent-register.gbo.test").
		WithEnvironment("GBO_CONSENT_AUDIENCE", "gbo:dvtp:pdp").
		WithEnvironment("PDP_BUNDLE_MANAGER", pdpBundleManager).
		WithEnvironment("PDP_TLS_CERT", "/certs/pdp-service.pem").
		WithEnvironment("PDP_TLS_KEY", "/certs/pdp-service-key.pem").
		// The image's logtee exports the PDP's console, the decision log
		// among it, as OTLP log records of this resource.
		WithEnvironment("OTEL_SERVICE_NAME", "openftv-pdp").
		WithEnvironment("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4317").
		WaitFor(otelCollector).
		WithBindMount(m.repo("policies"), "/policies", aspire.BindMountIsReadOnly(true)).
		WithBindMount(m.repo("schemas/pdp-mirror"), "/etc/gbo/ftv-graphql", aspire.BindMountIsReadOnly(true)).
		WithBindMount(m.repo("services/openftv-pdp/certs"), "/certs", aspire.BindMountIsReadOnly(true)).
		WithBindMount(m.repo("services/openftv-pdp/pip-pull"), "/pip-pull", aspire.BindMountIsReadOnly(true)).
		WaitForCompletion(policyCheck).
		WaitForCompletion(postgresFtvInit).
		WaitForCompletion(pdpCert).
		WaitFor(consentRegister).
		WaitFor(dvtpOnboardingRegister)

	// The source Managers decide on incoming contracts through the PDP, so
	// the PDP must answer with the seeded Hypotheek-BV admission before the
	// first contract is submitted (the PIP pull takes a few seconds after
	// start).
	pdpAdmission := m.container("pdp-admission", "gbo-demo/pki-tools", "local").
		WithImagePullPolicy(aspire.ImagePullPolicyNever).
		WithBindMount(m.repo("services/openftv-pdp/certs/pdp-service.pem"), "/pdp-ca.pem", aspire.BindMountIsReadOnly(true)).
		WithEntrypoint("bash").
		WithArgs([]string{"-c", strings.TrimSpace(`
request='{"subject":{"type":"peer_id","id":"99999999900000000200","properties":{"self_peer_id":"99999999900000000200","peer_ids":["99999999900000000200","99999999900000000300"]}},"action":{"name":"autosign_contract"},"resource":{"type":"contract","id":"pdp-admission-readiness","properties":{"grant_types":[2]}},"context":{"doelbinding":"auto_sign_contract"}}'
for i in $(seq 1 60); do
  if curl -fsS --max-time 2 --cacert /pdp-ca.pem -H 'Content-Type: application/json' -d "$request" https://openftv-pdp:8443/authzen/v1/evaluation | jq -e '.decision == true' >/dev/null 2>&1; then
    echo "OpenFTV admission data ready"; exit 0
  fi
  sleep 1
done
echo "OpenFTV did not load the seeded Hypotheek-BV admission within 60s" >&2; exit 1
`)}).
		WaitForCompletion(pkiToolsImage).
		WaitFor(openftvPdp)

	// ── Sources: Belastingdienst, RvIG, LVG and an unsecured demo source ─
	bsnkMock := goService("bsnk-mock", "Dockerfile", 4003, "../services/bsnk-mock", 0).
		WithEnvironment("BSN_AUTHORISED_OINS", bsnAuthorisedOins)

	decryptionComponent := goService("decryption-component", "Dockerfile", 4003, "../services/bsnk-mock", 0).
		WithEnvironment("DECRYPTION_COMPONENT_ONLY", "true")

	graphqlServer := goService("graphql-server", "services/graphql-server/Dockerfile", 4000, "", 0).
		WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata.json").
		WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-bd:4016").
		WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
		WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyBd).
		WithEnvironment("LDV_YEAR_ACTIVITY_TEMPLATE", "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-%d/v1").
		WithBindMount(m.repo("services/graphql-server/config"), "/config", aspire.BindMountIsReadOnly(true)).
		WithVolume("/data", aspire.VolumeName("gbo-ldv-outbox-graphql-server")).
		WaitFor(logboeken["logboek-bd"])

	brpGraphqlServer := goService("brp-graphql-server", "services/brp-graphql-server/Dockerfile", 4001, "", 0).
		WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata.json").
		WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-brp:4016").
		WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
		WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyBrp).
		WithBindMount(m.repo("services/brp-graphql-server/config"), "/config", aspire.BindMountIsReadOnly(true)).
		WithVolume("/data", aspire.VolumeName("gbo-ldv-outbox-brp-graphql-server")).
		WaitFor(logboeken["logboek-brp"])

	goService("unsecured-graphql-server", "services/graphql-server/Dockerfile", 4000, "", 0).
		WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata-unsecured.json").
		WithBindMount(m.repo("services/graphql-server/config"), "/config", aspire.BindMountIsReadOnly(true))

	// The source-side sidecar in front of a source: decrypts the polymorphic
	// identity and writes the source's logbook records.
	sidecar := func(name, upstream, logbook string, pseudonymKey aspire.ParameterResource, activityBase, activityPrefix string, source aspire.ContainerResource) aspire.ContainerResource {
		return goService(name, "services/bron-sidecar/Dockerfile", 4011, "", 0).
			WithEnvironment("UPSTREAM_URL", upstream).
			WithEnvironment("DECRYPTION_URL", "http://decryption-component:4003").
			WithEnvironment("SUBJECT_KEYS_DIR", "/app/config/demo-keys").
			WithEnvironment("OWN_PEER_OIN", "99999999900000000200").
			WithEnvironment("PSEUDONYM_VARS", "bsn").
			WithEnvironment("LDV_LOGBOOK_URL", fmt.Sprintf("http://%s:4016", logbook)).
			WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
			WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKey).
			WithEnvironment("LDV_DECRYPTION_ACTIVITY", fmt.Sprintf("%s/%s-identiteit-ontsleuteling/v1", activityBase, activityPrefix)).
			WithEnvironment("LDV_FORWARD_ACTIVITY", fmt.Sprintf("%s/%s-bronquery-doorgifte/v1", activityBase, activityPrefix)).
			WithVolume("/data", aspire.VolumeName("gbo-ldv-outbox-"+name)).
			WaitFor(source).
			WaitFor(decryptionComponent).
			WaitFor(logboeken[logbook])
	}

	sidecar("bron-sidecar", "http://graphql-server:4000", "logboek-bd", pseudonymKeyBd,
		"https://logboek.belastingdienst.nl/verwerkingsactiviteiten", "bd", graphqlServer)
	sidecar("brp-sidecar", "http://brp-graphql-server:4001", "logboek-brp", pseudonymKeyBrp,
		"https://logboek.rvig.nl/verwerkingsactiviteiten", "brp", brpGraphqlServer)

	var lvgGraphqlServer aspire.ContainerResource
	if on("dvtp") {
		lvgGraphqlServer = goService("lvg-graphql-server", "services/lvg-graphql-server/Dockerfile", 4008, "", 0).
			WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-lvg:4016").
			WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
			WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyLvg).
			WithEnvironment("LDV_ACTIVITY_BASE", "https://logboek.lvg.test/verwerkingsactiviteiten").
			WithVolume("/data", aspire.VolumeName("gbo-ldv-outbox-lvg-graphql-server")).
			WaitFor(logboeken["logboek-lvg"])
		sidecar("lvg-sidecar", "http://lvg-graphql-server:4008", "logboek-lvg", pseudonymKeyLvg,
			"https://logboek.lvg.test/verwerkingsactiviteiten", "lvg", lvgGraphqlServer)
	}

	// ── FSC network (OpenFSC v2.4.0) ─────────────────────────────────────
	// One Postgres for all peers, the group CA (cfssl + certportal), and per
	// peer a controller, manager, txlog-api and inway or outway, each with
	// its migrations. Certificates are made by the unchanged fsc-infra/pki
	// scripts, run on the host so the CA keys stay outside the service
	// containers.
	type fscPeer struct{ manager, controller aspire.ContainerResource }
	var fscPeers []fscPeer
	var fscReady aspire.ContainerResource

	if fscOn {
		const fscVersion = "v2.4.0"
		const directoryPeerID = "99999999900000000000"

		fscRootCa := m.executable("fsc-root-ca", "bash", m.repoRoot, "fsc-infra/pki/generate-root-ca.sh").
			WaitForCompletion(pkiToolsImage)

		cfssl := m.container("cfssl", "gbo-demo/pki-tools", "local").
			WithImagePullPolicy(aspire.ImagePullPolicyNever).
			WithBindMount(m.repo("fsc-infra/pki"), "/pki", aspire.BindMountIsReadOnly(true)).
			WithArgs([]string{"cfssl", "serve", "-address=0.0.0.0", "-port=8888",
				"-ca=/pki/ca/intermediate.pem", "-ca-key=/pki/ca/intermediate-key.pem",
				"-config=/pki/cfssl-signing-config.json"}).
			WaitForCompletion(fscRootCa)

		certportal := m.container("certportal", "federatedserviceconnectivity/ca-certportal", fscVersion).
			WithHttpEndpoint(aspire.HttpEndpointPort(8091), aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithArgs([]string{"/usr/local/bin/ca-certportal", "serve", "--listen-address=0.0.0.0:8080",
				"--ca-host=http://cfssl:8888", "--include-issuer-cert=true"}).
			WaitFor(cfssl)

		fscPostgres := m.container("fsc-postgres", "postgres", "17.10-alpine").
			WithContainerNetworkAlias("postgres").
			WithEndpoint(aspire.EndpointTargetPort(5432), aspire.EndpointName("tcp"), aspire.EndpointScheme("tcp")).
			WithEnvironment("POSTGRES_USER", "postgres").
			WithEnvironment("POSTGRES_PASSWORD", fscPostgresPassword).
			WithArgs([]string{"postgres", "-c", "max_connections=300"}).
			WithVolume("/var/lib/postgresql/data", aspire.VolumeName("gbo-fsc-postgres-data"))

		// Creates every peer database that does not exist yet, also in a
		// volume initialised before a peer was added.
		fscDatabases := m.container("fsc-databases", "postgres", "17.10-alpine").
			WithEnvironment("PGHOST", "postgres").
			WithEnvironment("PGUSER", "postgres").
			WithEnvironment("PGPASSWORD", fscPostgresPassword).
			WithEntrypoint("sh").
			WithArgs([]string{"-c", strings.TrimSpace(`
for i in $(seq 1 60); do psql -d postgres -tAc 'select 1' >/dev/null 2>&1 && break; sleep 1; done
for org in dir edi bd hv brp cr pdp ir int asip; do
  for part in controller manager txlog; do
    db="fsc_${org}_${part}"
    [ "$(psql -d postgres -tAc "select 1 from pg_database where datname='$db'")" = 1 ] || createdb "$db"
  done
done
echo "FSC databases ready"
`)}).
			WaitFor(fscPostgres)

		// One peer: certificates, migrations, controller, manager, txlog-api
		// and an inway (provider) or outway (consumer).
		type peerSpec struct {
			prefix, pkiDir, certName, bootstrap, db string
			controllerPort                          int
			inway, outway                           string
			authzen                                 bool
			managerArgs                             []string
			outwayPort                              int
		}
		addFscPeer := func(p peerSpec) {
			certs := m.executable(p.prefix+"-certs", "bash", m.repoRoot,
				"aspire/scripts/fsc-pki.sh", "fsc-infra/pki/"+p.bootstrap).
				WaitFor(certportal)

			fsc := func(name, component string) aspire.ContainerResource {
				return m.container(name, "federatedserviceconnectivity/"+component, fscVersion).
					WithBindMount(m.repo("fsc-infra/"+p.pkiDir), "/certs", aspire.BindMountIsReadOnly(true)).
					WithEnvironment("TLS_GROUP_ROOT_CERT", "/certs/org/root.pem").
					WithEnvironment("TLS_ROOT_CERT", "/certs/internal/intermediate_ca.pem").
					WithEnvironment("TLS_CERT", "/certs/internal/internal-cert.pem").
					WithEnvironment("TLS_KEY", "/certs/internal/internal-cert-key.pem").
					WithEnvironment("GROUP_ID", "fsc-demo").
					WaitForCompletion(certs)
			}
			dsn := func(part string) *aspire.ReferenceExpression {
				return aspire.RefExpr("postgresql://postgres:%v@postgres:5432/fsc_"+p.db+"_"+part+"?sslmode=disable&connect_timeout=2", fscPostgresPassword)
			}
			migrations := func(component, part string) aspire.ContainerResource {
				return fsc(p.prefix+"-migrations-"+component, component).
					WithArgs([]string{"/usr/local/bin/" + component, "migrate", "up"}).
					WithEnvironment("POSTGRES_DSN", dsn(part)).
					WaitForCompletion(fscDatabases)
			}

			migrateController := migrations("controller", "controller")
			migrateManager := migrations("manager", "manager")
			migrateTxlog := migrations("txlog-api", "txlog")

			txlog := fsc(p.prefix+"-txlog-api", "txlog-api").
				WithEnvironment("STORAGE_POSTGRES_DSN", dsn("txlog")).
				WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:9443").
				WithEnvironment("LISTEN_ADDRESS_PLAIN", "0.0.0.0:8080").
				WaitForCompletion(migrateTxlog)

			manager := fsc(p.prefix+"-manager", "manager").
				WithEnvironment("TLS_GROUP_CERT", "/certs/org/"+p.certName+".pem").
				WithEnvironment("TLS_GROUP_KEY", "/certs/org/"+p.certName+"-key.pem").
				WithEnvironment("TLS_GROUP_TOKEN_CERT", "/certs/org/"+p.certName+".pem").
				WithEnvironment("TLS_GROUP_TOKEN_KEY", "/certs/org/"+p.certName+"-key.pem").
				WithEnvironment("TLS_GROUP_CONTRACT_CERT", "/certs/org/"+p.certName+".pem").
				WithEnvironment("TLS_GROUP_CONTRACT_KEY", "/certs/org/"+p.certName+"-key.pem").
				WithEnvironment("TLS_INTERNAL_UNAUTHENTICATED_ROOT_CERT", "/certs/internal/intermediate_ca.pem").
				WithEnvironment("TLS_INTERNAL_UNAUTHENTICATED_CERT", "/certs/internal/internal-cert.pem").
				WithEnvironment("TLS_INTERNAL_UNAUTHENTICATED_KEY", "/certs/internal/internal-cert-key.pem").
				WithEnvironment("LISTEN_ADDRESS_INTERNAL", "0.0.0.0:9443").
				WithEnvironment("LISTEN_ADDRESS_INTERNAL_UNAUTHENTICATED", "0.0.0.0:9444").
				WithEnvironment("LISTEN_ADDRESS_EXTERNAL", "0.0.0.0:8443").
				WithEnvironment("CONTROLLER_REGISTRATION_API_ADDRESS", "https://"+p.prefix+"-controller:9443").
				WithEnvironment("TX_LOG_API_ADDRESS", "https://"+p.prefix+"-txlog-api:9443").
				WithEnvironment("SELF_ADDRESS", "https://"+p.prefix+"-manager:8443").
				WithEnvironment("DIRECTORY_MANAGER_ADDRESS", "https://directory-manager:8443").
				WithEnvironment("DIRECTORY_PEER_ID", directoryPeerID).
				WithEnvironment("STORAGE_POSTGRES_DSN", dsn("manager")).
				WaitForCompletion(migrateManager).
				WaitFor(txlog)
			args := append([]string{"/usr/local/bin/manager", "serve"}, p.managerArgs...)
			if p.authzen {
				// The Manager decides on incoming contracts through the PDP.
				args = append(args,
					"--authzen-auto-sign-pdp-address=https://openftv-pdp:8443/authzen/v1",
					"--authzen-auto-sign-pdp-root-ca=/authzen-ca/pdp-service.pem")
				manager.WithBindMount(m.repo("services/openftv-pdp/certs"), "/authzen-ca", aspire.BindMountIsReadOnly(true)).
					WaitForCompletion(pdpAdmission)
			}
			manager.WithArgs(args)

			controller := fsc(p.prefix+"-controller", "controller").
				WithHttpEndpoint(aspire.HttpEndpointPort(p.controllerPort), aspire.HttpEndpointTargetPort(3001), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
				WithEnvironment("LISTEN_ADDRESS_UI", "0.0.0.0:3001").
				WithEnvironment("LISTEN_ADDRESS_REGISTRATION_API", "0.0.0.0:9443").
				WithEnvironment("LISTEN_ADDRESS_ADMINISTRATION_API", "0.0.0.0:9444").
				WithEnvironment("MANAGER_ADDRESS_INTERNAL", "https://"+p.prefix+"-manager:9443").
				WithEnvironment("DIRECTORY_ADDRESS", "https://directory-manager:8443").
				WithEnvironment("STORAGE_POSTGRES_DSN", dsn("controller")).
				WithEnvironment("AUDITLOG_TYPE", "stdout").
				WaitForCompletion(migrateController).
				WaitFor(manager)

			if p.inway != "" {
				gw := fsc(p.prefix+"-inway", "inway").
					WithEnvironment("TLS_GROUP_CERT", "/certs/org/"+p.certName+".pem").
					WithEnvironment("TLS_GROUP_KEY", "/certs/org/"+p.certName+"-key.pem").
					WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:443").
					WithEnvironment("NAME", p.inway).
					WithEnvironment("SELF_ADDRESS", "https://"+p.prefix+"-inway:443").
					WithEnvironment("CONTROLLER_REGISTRATION_API_ADDRESS", "https://"+p.prefix+"-controller:9443").
					WithEnvironment("TX_LOG_API_ADDRESS", "https://"+p.prefix+"-txlog-api:9443").
					WithEnvironment("MANAGER_INTERNAL_UNAUTHENTICATED_ADDRESS", "https://"+p.prefix+"-manager:9444").
					WaitFor(controller)
				if p.authzen && p.prefix != "cr" {
					// The Inway asks the PDP per request, with the body.
					gw.WithBindMount(m.repo("services/openftv-pdp/certs"), "/authzen-ca", aspire.BindMountIsReadOnly(true)).
						WithEnvironment("AUTHZEN_SERVICE_ADDRESS", "https://openftv-pdp:8443/authzen/v1").
						WithEnvironment("AUTHZEN_ROOT_CA", "/authzen-ca/pdp-service.pem").
						WithEnvironment("AUTHZEN_WITH_BODY", "true")
				}
			}
			if p.outway != "" {
				gw := fsc(p.prefix+"-outway", "outway").
					WithEnvironment("TLS_GROUP_CERT", "/certs/org/"+p.certName+".pem").
					WithEnvironment("TLS_GROUP_KEY", "/certs/org/"+p.certName+"-key.pem").
					WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:8080").
					WithEnvironment("NAME", p.outway).
					WithEnvironment("CONTROLLER_REGISTRATION_API_ADDRESS", "https://"+p.prefix+"-controller:9443").
					WithEnvironment("TX_LOG_API_ADDRESS", "https://"+p.prefix+"-txlog-api:9443").
					WithEnvironment("MANAGER_INTERNAL_ADDRESS", "https://"+p.prefix+"-manager:9443").
					WithEnvironment("ENABLE_GRANT_HASH_SUGGESTION", "true").
					WaitFor(controller)
				if p.outwayPort != 0 {
					// The host provisioning steps reach the EDI Outway here.
					gw.WithHttpEndpoint(aspire.HttpEndpointPort(p.outwayPort), aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false))
				}
			}
			fscPeers = append(fscPeers, fscPeer{manager, controller})
			if p.prefix == "directory" {
				m.directoryManager = manager
			}
		}

		addFscPeer(peerSpec{prefix: "directory", pkiDir: "directory-peer/pki", certName: "directory-peer", bootstrap: "bootstrap-directory-peer.sh", db: "dir", controllerPort: 8092,
			inway: "DirectoryInway-01", managerArgs: []string{"--auto-sign-grants=servicePublication"}})
		addFscPeer(peerSpec{prefix: "edi", pkiDir: "orgs/edi-issuer/pki", certName: "edi-issuer", bootstrap: "bootstrap-edi-issuer.sh", db: "edi", controllerPort: 8094,
			outway: "EdiOutway-01", outwayPort: 8087})
		addFscPeer(peerSpec{prefix: "bd", pkiDir: "orgs/belastingdienst-mock/pki", certName: "bd-mock", bootstrap: "bootstrap-bd-mock.sh", db: "bd", controllerPort: 8095,
			inway: "BdInway-01", authzen: true})
		addFscPeer(peerSpec{prefix: "hv", pkiDir: "orgs/hypotheekverlener-mock/pki", certName: "hypotheekverlener", bootstrap: "bootstrap-hypotheekverlener.sh", db: "hv", controllerPort: 8096,
			outway: "HvOutway-01"})
		addFscPeer(peerSpec{prefix: "cr", pkiDir: "orgs/consent-register/pki", certName: "consent-register", bootstrap: "bootstrap-consent-register.sh", db: "cr", controllerPort: 8098,
			inway: "CrInway-01", authzen: true})
		addFscPeer(peerSpec{prefix: "pdp", pkiDir: "orgs/gbo-pdp/pki", certName: "gbo-pdp", bootstrap: "bootstrap-gbo-pdp.sh", db: "pdp", controllerPort: 8099,
			outway: "PdpOutway-01"})
		addFscPeer(peerSpec{prefix: "ir", pkiDir: "orgs/installatieregister/pki", certName: "installatieregister", bootstrap: "bootstrap-installatieregister.sh", db: "ir", controllerPort: 8100,
			outway: "IrOutway-01"})
		if on("full") {
			// The separate BRP/RvIG provider peer (...0400). No demo flow uses
			// it: the demo publishes BRP through the BD participant (...0200).
			addFscPeer(peerSpec{prefix: "brp", pkiDir: "orgs/brp-mock/pki", certName: "brp-mock", bootstrap: "bootstrap-brp-mock.sh", db: "brp", controllerPort: 8097,
				inway: "BrpInway-01", authzen: true})
		}
		if on("integrator") {
			addFscPeer(peerSpec{prefix: "int", pkiDir: "orgs/integrator/pki", certName: "integrator", bootstrap: "bootstrap-integrator.sh", db: "int", controllerPort: 8101,
				outway: "IntOutway-01"})
		}
		if on("asi") {
			addFscPeer(peerSpec{prefix: "asip", pkiDir: "orgs/asip/pki", certName: "asip", bootstrap: "bootstrap-asip.sh", db: "asip", controllerPort: 8102,
				outway: "AsipOutway-01"})
		}

		// Its certificates come from the directory peer's bootstrap.
		m.container("directory-ui", "federatedserviceconnectivity/directory-ui", fscVersion).
			WithHttpEndpoint(aspire.HttpEndpointPort(8093), aspire.HttpEndpointTargetPort(3001), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithBindMount(m.repo("fsc-infra/directory-ui/pki"), "/certs", aspire.BindMountIsReadOnly(true)).
			WithEnvironment("TLS_GROUP_ROOT_CERT", "/certs/org/root.pem").
			WithEnvironment("GROUP_ID", "fsc-demo").
			WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:3001").
			WithEnvironment("DIRECTORY_MANAGER_ADDRESS", "https://directory-manager:8443").
			WithEnvironment("TLS_GROUP_CERT", "/certs/org/directory-ui.pem").
			WithEnvironment("TLS_GROUP_KEY", "/certs/org/directory-ui-key.pem").
			WaitFor(m.directoryManager)

		// ── Contract seeds ───────────────────────────────────────────────
		// Services, publications, connection contracts and grant links over
		// mTLS against the Managers and Controllers, in the order the
		// Makefile ran them. Each one waits for the previous.
		var previous aspire.ContainerResource
		seed := func(name, script string, env ...[2]string) {
			s := pkiStep(name, script, env...)
			for _, peer := range fscPeers {
				s.WaitFor(peer.manager).WaitFor(peer.controller)
			}
			if previous != nil {
				s.WaitForCompletion(previous)
			}
			previous = s
			m.seeds = append(m.seeds, name)
		}

		// BD publishes bri (BD source); the EDI issuer connects to it.
		seed("fsc-seed-bri", "scripts/seed-bri-contract.sh")
		if on("dvtp") {
			// Hypotheek-BV (the DvTP consumer) connects to bri.
			seed("fsc-seed-bri-hv", "scripts/seed-bri-connection-hv.sh")
			// The PDP asks the consent register for a consent's status over FSC.
			seed("fsc-seed-consent-status", "scripts/seed-bri-contract.sh",
				[2]string{"SERVICE_NAME", "consent-status"},
				[2]string{"SERVICE_ENDPOINT_URL", "http://consent-register:4012"},
				[2]string{"SERVICE_INWAY_ADDRESS", "https://cr-inway:443"},
				[2]string{"PROVIDER_PEER_ID", "99999999900000000700"},
				[2]string{"PROVIDER_CONTROLLER_URL", "https://cr-controller:9444"},
				[2]string{"PROVIDER_MANAGER_URL", "https://cr-manager:9443"},
				[2]string{"PROVIDER_INTERNAL_DIR", "/work/orgs/consent-register/pki/internal"},
				[2]string{"CONSUMER_PEER_ID", "99999999900000000800"},
				[2]string{"CONSUMER_MANAGER_URL", "https://pdp-manager:9443"},
				[2]string{"CONSUMER_INTERNAL_DIR", "/work/orgs/gbo-pdp/pki/internal"},
				[2]string{"CONSUMER_ORG_CERT", "/work/orgs/gbo-pdp/pki/org/gbo-pdp.pem"},
				[2]string{"CONSUMER_CONTROLLER_DB", "fsc_pdp_controller"},
				[2]string{"GRANT_LINK_PATH", "/consent-status"},
				[2]string{"OUTWAY_NAME", "PdpOutway-01"})
			// LVG on the shared provider peer; the Installatie Register consumes it.
			seed("fsc-seed-lvg", "scripts/seed-bri-contract.sh",
				[2]string{"SERVICE_NAME", "lvg"},
				[2]string{"SERVICE_ENDPOINT_URL", "http://lvg-sidecar:4011"},
				[2]string{"CONSUMER_PEER_ID", "99999999900000001000"},
				[2]string{"CONSUMER_MANAGER_URL", "https://ir-manager:9443"},
				[2]string{"CONSUMER_INTERNAL_DIR", "/work/orgs/installatieregister/pki/internal"},
				[2]string{"CONSUMER_ORG_CERT", "/work/orgs/installatieregister/pki/org/installatieregister.pem"},
				[2]string{"CONSUMER_CONTROLLER_DB", "fsc_ir_controller"},
				[2]string{"GRANT_LINK_PATH", "/lvg"},
				[2]string{"OUTWAY_NAME", "IrOutway-01"})
		}
		if on("onboarding") {
			// The logical RvIG source and the two metadata services on the BD peer.
			seed("fsc-seed-rvig-source", "scripts/seed-bri-contract.sh",
				[2]string{"SERVICE_NAME", "brp"}, [2]string{"SERVICE_ENDPOINT_URL", "http://brp-sidecar:4011"}, [2]string{"CREATE_GRANT_LINK", "false"})
			seed("fsc-seed-metadata-bd", "scripts/seed-bri-contract.sh",
				[2]string{"SERVICE_NAME", "gbo-metadata-bd"}, [2]string{"SERVICE_ENDPOINT_URL", "http://graphql-server:4000"}, [2]string{"CREATE_GRANT_LINK", "false"})
			seed("fsc-seed-metadata-rvig", "scripts/seed-bri-contract.sh",
				[2]string{"SERVICE_NAME", "gbo-metadata-rvig"}, [2]string{"SERVICE_ENDPOINT_URL", "http://brp-graphql-server:4001"}, [2]string{"CREATE_GRANT_LINK", "false"})
		}
		if on("integrator") {
			// The integrator acts for Hypotheek-BV over a delegated connection.
			seed("fsc-seed-int", "scripts/seed-bri-delegation-int.sh")
		}
		if on("asi") {
			// The ASIP connects to the BRP source through a grant link at /brp.
			seed("fsc-seed-asip", "scripts/seed-bri-contract.sh",
				[2]string{"SERVICE_NAME", "brp"},
				[2]string{"SERVICE_ENDPOINT_URL", "http://brp-sidecar:4011"},
				[2]string{"CONSUMER_PEER_ID", "99999999900000001200"},
				[2]string{"CONSUMER_MANAGER_URL", "https://asip-manager:9443"},
				[2]string{"CONSUMER_INTERNAL_DIR", "/work/orgs/asip/pki/internal"},
				[2]string{"CONSUMER_ORG_CERT", "/work/orgs/asip/pki/org/asip.pem"},
				[2]string{"CONSUMER_CONTROLLER_DB", "fsc_asip_controller"},
				[2]string{"GRANT_LINK_PATH", "/brp"},
				[2]string{"OUTWAY_NAME", "AsipOutway-01"})
		}
		fscReady = previous
	}

	// afterFsc waits until the FSC contracts are in place, when FSC runs.
	afterFsc := func(r aspire.ContainerResource) aspire.ContainerResource {
		if fscReady != nil {
			r.WaitForCompletion(fscReady)
		}
		return r
	}

	// ── Developer portal ─────────────────────────────────────────────────
	ldvLogbooks := strings.Join([]string{
		"https://logboek.hypotheek-bv.test/data-processing-operations=Hypotheek-BV=http://logboek-afnemer:4016=start",
		"https://logboek.gbo.overheid.nl/eudi-adapter/data-processing-operations=GBO EUDI-adapter=http://logboek-eudi-adapter:4016=start",
		"https://logboek.belastingdienst.nl/data-processing-operations=Belastingdienst=http://logboek-bd:4016",
		"https://logboek.rvig.nl/data-processing-operations=RvIG=http://logboek-brp:4016",
		"https://logboek.gbo.overheid.nl/toestemming/data-processing-operations=GBO toestemming=http://logboek-toestemming:4016",
		"https://logboek.installatieregister.test/data-processing-operations=Installatie Register=http://logboek-ir:4016=start",
		"https://logboek.lvg.test/data-processing-operations=LVG=http://logboek-lvg:4016",
	}, ",")

	devPortalBackend := m.dockerfile("dev-portal-backend", "..", "services/dev-portal-backend/Dockerfile").
		WithHttpEndpoint(aspire.HttpEndpointTargetPort(4007), aspire.HttpEndpointName("http")).
		WithHttpHealthCheck(aspire.HttpHealthCheckPath("/health")).
		WithEnvironment("OTEL_SERVICE_NAME", "dev-portal-backend").
		// Traces and the PDP's decision log, from the dashboard's telemetry API.
		WithContainerRuntimeArgs([]string{"--add-host", "host.docker.internal:host-gateway"}).
		WithEnvironment("TELEMETRY_API_URL", dashboardURL).
		WithEnvironment("TELEMETRY_API_KEY", keys["AppHost:DashboardApiKey"]).
		WithEnvironment("CITIZENS_FILE", "/citizens/citizens.json").
		WithEnvironment("ORGANIZATIONS_FILE", "/organizations.json").
		WithEnvironment("LDV_LOGBOOKS", ldvLogbooks).
		WithEnvironment("LDV_READ_TOKEN", ldvReadToken).
		WithEnvironment("QTSP_URL", "http://qtsp-reference:4023").
		WithEnvironment("TEST_PID_OFFERS_URL", "http://eudi-credential-issuer:8081/offers").
		WithEnvironment("TEST_PID_PERSONS_FILE", "/test-pid-persons/personen.json").
		WithEnvironment("CREDENTIAL_ISSUER_STATUS_URL", "http://eudi-credential-issuer:8080").
		WithEnvironment("EUDI_OFFERS_URL", "http://eudi-adapter:4009/eudi-offers.json").
		WithEnvironment("FSC_GROUP_ID", "fsc-demo").
		WithEnvironment("POLICIES_DIR", "/policies").
		WithEnvironment("RULES_FILE", "/rules.json").
		WithEnvironment("ADL_DATABASE_URL", aspire.RefExpr("postgres://adl_reader:%v@postgres-ftv:5432/ftv_adl?sslmode=disable", ftvAdlReaderPassword)).
		WithVolume("/var", aspire.VolumeName("gbo-dev-portal-var")).
		WithBindMount(m.repo("services/graphql-server/mockdata"), "/citizens", aspire.BindMountIsReadOnly(true)).
		WithBindMount(m.repo("services/brp-graphql-server/mockdata"), "/test-pid-persons", aspire.BindMountIsReadOnly(true)).
		WithBindMount(m.repo("services/dev-portal-backend/organizations.json"), "/organizations.json", aspire.BindMountIsReadOnly(true)).
		WithBindMount(m.repo("policies"), "/policies", aspire.BindMountIsReadOnly(true)).
		WithBindMount(m.repo("services/dev-portal-backend/rules.json"), "/rules.json", aspire.BindMountIsReadOnly(true)).
		WaitFor(openftvPdp).
		WaitForCompletion(postgresFtvInit)

	// The FSC transaction logs it reads, per peer, with that peer's internal
	// certificates. The directories exist once the peer's bootstrap ran.
	for _, od := range [][2]string{{"edi", "edi-issuer"}, {"hv", "hypotheekverlener-mock"}, {"ir", "installatieregister"}, {"asip", "asip"}, {"bd", "belastingdienst-mock"}} {
		org, dir := od[0], od[1]
		mkdirAll(m.repo("fsc-infra/orgs/" + dir + "/pki/internal"))
		upper := strings.ToUpper(org)
		devPortalBackend.
			WithEnvironment("FSC_TXLOG_"+upper+"_URL", "https://"+org+"-txlog-api:9443").
			WithEnvironment("FSC_TXLOG_"+upper+"_CERT", "/certs/"+org+"/internal-cert.pem").
			WithEnvironment("FSC_TXLOG_"+upper+"_KEY", "/certs/"+org+"/internal-cert-key.pem").
			WithEnvironment("FSC_TXLOG_"+upper+"_CA", "/certs/"+org+"/intermediate_ca.pem").
			WithBindMount(m.repo("fsc-infra/orgs/"+dir+"/pki/internal"), "/certs/"+org, aspire.BindMountIsReadOnly(true))
	}

	// ── Consumers and the consent portal (dvtp) ──────────────────────────
	var consentPortalBackend, dienstverlenerBackend, irBackend aspire.ContainerResource
	if on("dvtp") {
		consentPortalBackend = goService("consent-portal-backend", "services/consent-portal-backend/Dockerfile", 4005, "", 0).
			WithEnvironment("BSNK_URL", "http://bsnk-mock:4003").
			WithEnvironment("CONSENT_URL", "http://consent-register:4002").
			WithEnvironment("DEV_PORTAL_BACKEND_URL", "http://dev-portal-backend:4007").
			WithEnvironment("CONSENT_SOURCES", "99999999900000000200@20260101").
			WithEnvironment("BSN_AUTHORISATION_LIST_MAX_AGE", "1h").
			WithEnvironment("SUBJECT_REF_KEY", subjectRefKey).
			WithEnvironment("SUBJECT_REF_KEY_VERSION", "1").
			WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-toestemming:4016").
			WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
			WithEnvironment("LDV_BSNK_NEXT_LOGBOOK_ID", "https://github.com/ICTU/GBO-demo/tree/main/services/bsnk-mock").
			WithVolume("/data", aspire.VolumeName("gbo-ldv-outbox-consent-portal-backend")).
			WaitFor(bsnkMock).
			WaitFor(consentRegister).
			WaitFor(logboeken["logboek-toestemming"])

		// A consumer backend, calling its source through its own FSC Outway.
		consumer := func(name string, port int, outway, outwayPath, sector, logbook, nextLogbooks, queryKind string) aspire.ContainerResource {
			c := goService(name, "services/dienstverlener-backend/Dockerfile", port, "", 0).
				WithEnvironment("PORT", strconv.Itoa(port)).
				WithEnvironment("OUTWAY_URL", fmt.Sprintf("http://%s:8080", outway)).
				WithEnvironment("OUTWAY_PATH", outwayPath).
				WithEnvironment("ORG_SECTOR", sector).
				WithEnvironment("DEV_PORTAL_BACKEND_URL", "http://dev-portal-backend:4007").
				WithEnvironment("LDV_LOGBOOK_URL", fmt.Sprintf("http://%s:4016", logbook)).
				WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
				WithEnvironment("LDV_NEXT_LOGBOOK_IDS", nextLogbooks).
				WithVolume("/data", aspire.VolumeName("gbo-ldv-outbox-"+name)).
				WaitFor(logboeken[logbook])
			if queryKind != "" {
				c.WithEnvironment("QUERY_KIND", queryKind)
			}
			return afterFsc(c)
		}

		dienstverlenerBackend = consumer("dienstverlener-backend", 4006, "hv-outway", "/bri/graphql", "hypotheekverlener",
			"logboek-afnemer", "bd=https://logboek.belastingdienst.nl/data-processing-operations", "")
		irBackend = consumer("ir-backend", 4010, "ir-outway", "/lvg/graphql", "installatieregister",
			"logboek-ir", "lvg=https://logboek.lvg.test/data-processing-operations", "lvg")
		if on("integrator") {
			consumer("integrator-backend", 4023, "int-outway", "/bri/graphql", "hypotheekverlener",
				"logboek-afnemer", "bd=https://logboek.belastingdienst.nl/data-processing-operations", "")
		}
	}

	// ── EUDI: source registry, onboarding, issuance (onboarding, eudi) ───
	eudiUlBase := m.configOr("Gbo:EudiUlBase", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance")

	// Says in the dashboard which EUDI parts were left out, and why.
	if len(eudiNotices) > 0 {
		m.executable("eudi-not-configured", "bash", m.repoRoot, "-c", `printf '%s\n' "$NOTICE" >&2; exit 1`).
			WithEnvironment("NOTICE", strings.Join(eudiNotices, "\n\n"))
	}

	// A host step: `go run .` in a Go module, skipped when its output exists.
	hostGoStep := func(name, module, skipWhenExists string, args ...any) aspire.ExecutableResource {
		if skipWhenExists != "" && fileExists(skipWhenExists) {
			return m.executable(name, "true", m.repoRoot)
		}
		return m.executableArgs(name, "go", m.repo("services/"+module), append([]any{"run", "."}, args...)...)
	}

	var sourceRegistryMigrations, eudiAdapter aspire.ContainerResource
	var eudiPublicURL aspire.ParameterResource
	var sourceRegistryReaderURL *aspire.ReferenceExpression

	if on("onboarding") {
		eudiPostgresPassword := m.generated("eudi-postgres-password")
		sourceRegistryPassword := m.generated("source-registry-password")
		sourceRegistryReaderPassword := m.generated("source-registry-reader-password")
		// Public HTTPS URLs: where the wallet reaches the issuance server, and
		// where the issuance server reaches the EUDI adapter. The issuance
		// server's host is the OpenID4VP client_id and a DNS SAN of every
		// reader certificate.
		eudiPublicURL = b.AddParameter("eudi-public-url").
			WithDescription("Public HTTPS URL where the wallet reaches the issuance server, e.g. https://eudi-is.example.org/")
		eudiBriURL := b.AddParameter("eudi-bri-url").
			WithDescription("Public HTTPS URL where the issuance server reaches the EUDI adapter, e.g. https://eudi-bri.example.org/")

		postgresEudi := m.container("postgres-eudi", "postgres", "16-alpine").
			WithEndpoint(aspire.EndpointTargetPort(5432), aspire.EndpointName("tcp"), aspire.EndpointScheme("tcp")).
			WithEnvironment("POSTGRES_USER", "wallet").
			WithEnvironment("POSTGRES_DB", "issuance_server").
			WithEnvironment("POSTGRES_PASSWORD", eudiPostgresPassword).
			WithVolume("/var/lib/postgresql/data", aspire.VolumeName("gbo-postgres-eudi-data"))

		sourceRegistryBootstrap := m.container("source-registry-bootstrap", "postgres", "16-alpine").
			WithEnvironment("PGHOST", "postgres-eudi").
			WithEnvironment("PGPORT", "5432").
			WithEnvironment("PGUSER", "wallet").
			WithEnvironment("PGPASSWORD", eudiPostgresPassword).
			WithEnvironment("SOURCE_REGISTRY_PASSWORD", sourceRegistryPassword).
			WithEnvironment("SOURCE_REGISTRY_READER_PASSWORD", sourceRegistryReaderPassword).
			WithBindMount(m.repo("scripts/bootstrap-source-registry.sh"), "/scripts/bootstrap-source-registry.sh", aspire.BindMountIsReadOnly(true)).
			WithEntrypoint("sh").
			WithArgs([]string{"-c", "for i in $(seq 1 60); do pg_isready -q && break; sleep 1; done; /scripts/bootstrap-source-registry.sh"}).
			WaitFor(postgresEudi)

		sourceRegistryReaderURL = aspire.RefExpr("postgres://source_registry_reader:%v@postgres-eudi:5432/source_registry", sourceRegistryReaderPassword)
		sourceRegistryURL := aspire.RefExpr("postgres://source_registry:%v@postgres-eudi:5432/source_registry", sourceRegistryPassword)

		// The eudi-adapter image doubles as the CLI for migrations,
		// materialisation, reconciliation and exports.
		eudiAdapterCli := func(name string, args ...any) aspire.ContainerResource {
			return m.containerArgs(m.dockerfile(name, "..", "services/eudi-adapter/Dockerfile"), args...).
				WithEnvironment("SOURCE_REGISTRY_SCHEMA", "source_registry")
		}

		sourceRegistryMigrations = eudiAdapterCli("source-registry-migrations", "./eudi-adapter", "migrate-source-registry").
			WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", sourceRegistryURL).
			WithEnvironment("SOURCE_REGISTRY_READER_ROLE", "source_registry_reader").
			WaitForCompletion(sourceRegistryBootstrap)

		// Development leaves for the three demo sources, issued on the host by
		// the pre-provisioned development CAs (make onboard-demo-sources).
		var provisionSteps []aspire.ExecutableResource
		for _, s := range [][4]string{
			{"belastingdienst", "99999999900000000200", "Belastingdienst", "assets/issuer-logos/belastingdienst.svg"},
			{"rvig", "99999999900000000200", "RvIG", "assets/issuer-logos/rvig.svg"},
			{"demo-unsecured", "99999999900000000900", "Demo unsecured source", "assets/issuer-logos/belastingdienst.svg"},
		} {
			id, oin, sourceName, logo := s[0], s[1], s[2], s[3]
			provisionSteps = append(provisionSteps, hostGoStep("provision-"+id+"-certificates", "eudi-adapter", "",
				"provision-development-certificates",
				"--source-id="+id, "--source-oin="+oin, "--source-name="+sourceName,
				"--source-description=", "--source-logo="+m.repo(logo),
				aspire.RefExpr("--reader-public-url=%v", eudiPublicURL),
				"--secrets-dir="+secretsDir, "--public-certificates-dir="+publicCertificatesDir))
		}

		// Publishes, transports through FSC, verifies and activates the source
		// metadata (make reconcile-sources).
		sourceReconciler := eudiAdapterCli("source-reconciler",
			"./eudi-adapter", "reconcile-sources", "--once",
			"--manager-url=https://edi-manager:9443",
			"--manager-ca=/fsc-certs/intermediate_ca.pem",
			"--manager-cert=/fsc-certs/internal-cert.pem",
			"--manager-key=/fsc-certs/internal-cert-key.pem",
			"--consumer-peer-id=99999999900000000100",
			"--outway-url=http://edi-outway:8080",
			"--schema=/config/schemas/gbo-source-metadata-v1.schema.json",
			aspire.RefExpr("--type-metadata-base-url=%v", eudiBriURL),
			"--storage-backend=postgres",
			"--certificate-store=public-filesystem",
			"--database-schema=source_registry",
			"--secrets-dir=/public-certificates",
			aspire.RefExpr("--reader-public-url=%v", eudiPublicURL),
			"--sources-dir=/config/sources",
			"--metadata-dir=/config/source-metadata",
			"--auto-promote").
			WithEnvironment("EUDI_PUBLIC_URL", eudiPublicURL).
			WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", sourceRegistryURL).
			WithBindMount(m.repo("schemas"), "/config/schemas", aspire.BindMountIsReadOnly(true)).
			WithBindMount(m.repo("sources"), "/config/sources", aspire.BindMountIsReadOnly(true)).
			WithBindMount(m.repo("source-metadata"), "/config/source-metadata", aspire.BindMountIsReadOnly(true)).
			WithBindMount(m.repo("fsc-infra/orgs/edi-issuer/pki/internal"), "/fsc-certs", aspire.BindMountIsReadOnly(true)).
			WithBindMount(publicCertificatesDir, "/public-certificates", aspire.BindMountIsReadOnly(true)).
			WaitForCompletion(sourceRegistryMigrations).
			WaitFor(graphqlServer).
			WaitFor(brpGraphqlServer)
		afterFsc(sourceReconciler)
		for _, step := range provisionSteps {
			sourceReconciler.WaitForCompletion(step)
		}

		if on("eudi") {
			// Reader, issuer, status-list and internal callback TLS
			// certificates of the reference QTSP.
			qtspIssuanceCertificates := hostGoStep("provision-qtsp-issuance-certificates", "eudi-adapter", "",
				"provision-qtsp-issuance-certificates",
				"--qtsp-oin=99999999900000001300", "--qtsp-name=GBO demo test QTSP",
				aspire.RefExpr("--reader-public-url=%v", eudiPublicURL),
				"--secrets-dir="+secretsDir, "--tls-host=qtsp-reference")

			// No published image: built from the pinned nl-wallet submodule.
			issuanceServerImage := m.executable("eudi-issuance-server-image", "bash", m.repoRoot,
				"aspire/scripts/nlwallet-image.sh", "gbo/eudi-issuance-server:dev", "services/eudi-issuance-server/Dockerfile")

			issuanceDatabaseURL := aspire.RefExpr("postgres://wallet:%v@postgres-eudi:5432/issuance_server", eudiPostgresPassword)
			eudiMigrations := m.container("eudi-migrations", "gbo/eudi-issuance-server", "dev").
				WithImagePullPolicy(aspire.ImagePullPolicyNever).
				WithEntrypoint("/issuance_server_migrations").
				WithArgs([]string{"up"}).
				WithEnvironment("DATABASE_URL", issuanceDatabaseURL).
				WaitForCompletion(issuanceServerImage).
				WaitForCompletion(sourceRegistryBootstrap)

			// Renders the issuance server's configuration from the active
			// Source Release, into a host directory the server then mounts.
			issuanceRuntime := m.repo(".aspire-data/eudi-issuance-runtime")
			mkdirAll(issuanceRuntime)
			eudiIssuanceMaterialize := eudiAdapterCli("eudi-issuance-materialize",
				"./eudi-adapter", "materialize-issuance-release",
				"--database-schema=source_registry",
				"--secrets-dir=/source-certificates",
				"--template=/app/issuance_server.toml.example",
				aspire.RefExpr("--adapter-base-url=%v", eudiBriURL),
				"--qtsp-attestation-url=https://qtsp-reference:4443/attestations",
				"--qtsp-type-metadata=/app/qtsp_attestation_metadata.json",
				"--output=/runtime/issuance_server.toml").
				WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", sourceRegistryReaderURL).
				WithEnvironment("EUDI_PUBLIC_URL", eudiPublicURL).
				WithBindMount(secretsDir, "/source-certificates", aspire.BindMountIsReadOnly(true)).
				WithBindMount(issuanceRuntime, "/runtime").
				WaitForCompletion(sourceReconciler).
				WaitForCompletion(qtspIssuanceCertificates)

			m.container("eudi-issuance-server", "gbo/eudi-issuance-server", "dev").
				WithImagePullPolicy(aspire.ImagePullPolicyNever).
				WithContainerRuntimeArgs([]string{"--user", "0"}).
				WithHttpEndpoint(aspire.HttpEndpointPort(18001), aspire.HttpEndpointTargetPort(18001), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
				WithEnvironment("ISSUANCE_SERVER__STORAGE__URL", issuanceDatabaseURL).
				WithEnvironment("ISSUANCE_SERVER__UNIVERSAL_LINK_BASE_URL", "walletdebuginteraction://wallet.edi.rijksoverheid.nl/").
				WithEnvironment("ISSUANCE_SERVER__WALLET_SERVER__IP", "0.0.0.0").
				WithEnvironment("ISSUANCE_SERVER__WALLET_SERVER__PORT", "18001").
				WithEnvironment("ISSUANCE_SERVER__INTERNAL_SERVER__IP", "0.0.0.0").
				WithEnvironment("ISSUANCE_SERVER__INTERNAL_SERVER__PORT", "18002").
				WithEnvironment("ISSUANCE_SERVER__PUBLIC_URL", eudiPublicURL).
				WithBindMount(filepath.Join(issuanceRuntime, "issuance_server.toml"), "/issuance_server.toml", aspire.BindMountIsReadOnly(true)).
				WithBindMount(issuanceRuntime, "/config", aspire.BindMountIsReadOnly(true)).
				WithVolume("/tsl-publish", aspire.VolumeName("gbo-eudi-tsl-publish")).
				WaitForCompletion(eudiMigrations).
				WaitForCompletion(eudiIssuanceMaterialize)

			eudiAdapter = goService("eudi-adapter", "services/eudi-adapter/Dockerfile", 4009, "", 9409).
				WithEnvironment("FSC_OUTWAY_URL", "http://edi-outway:8080").
				WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", sourceRegistryReaderURL).
				WithEnvironment("SOURCE_REGISTRY_SCHEMA", "source_registry").
				WithEnvironment("SOURCE_REGISTRY_REFRESH_INTERVAL", "2s").
				WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-eudi-adapter:4016").
				WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken).
				WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyGbo).
				WithEnvironment("LDV_NEXT_LOGBOOK_IDS", "belastingdienst=https://logboek.belastingdienst.nl/data-processing-operations,rvig=https://logboek.rvig.nl/data-processing-operations").
				WithVolume("/data", aspire.VolumeName("gbo-ldv-outbox-eudi-adapter")).
				WaitForCompletion(sourceReconciler).
				WaitFor(logboeken["logboek-eudi-adapter"])
		}

		if on("credential-issuer") {
			// Own credential issuer beside the issuance server (make credential-issuer-up).
			issuerPublicURL := b.AddParameter("issuer-public-url").
				WithDescription("Public URL of the own credential issuer, e.g. https://eudi-issuer.example.org")
			trustPublicURL := b.AddParameter("eudi-trust-public-url").
				WithDescription("Public URL of the trust host, e.g. https://eudi-trust.example.org")
			credentialIssuerDbPassword := m.generated("credential-issuer-db-password")

			credentialIssuerDbBootstrap := m.container("credential-issuer-db-bootstrap", "postgres", "16-alpine").
				WithEnvironment("PGHOST", "postgres-eudi").
				WithEnvironment("PGPORT", "5432").
				WithEnvironment("PGUSER", "wallet").
				WithEnvironment("PGPASSWORD", eudiPostgresPassword).
				WithEnvironment("CREDENTIAL_ISSUER_DB_PASSWORD", credentialIssuerDbPassword).
				WithBindMount(m.repo("scripts/bootstrap-credential-issuer.sh"), "/scripts/bootstrap-credential-issuer.sh", aspire.BindMountIsReadOnly(true)).
				WithEntrypoint("sh").
				WithArgs([]string{"-c", "for i in $(seq 1 60); do pg_isready -q && break; sleep 1; done; /scripts/bootstrap-credential-issuer.sh"}).
				WaitFor(postgresEudi)
			issuerDbURL := aspire.RefExpr("postgres://credential_issuer:%v@postgres-eudi:5432/credential_issuer", credentialIssuerDbPassword)
			credentialIssuerMigrations := m.dockerfile("credential-issuer-migrations", "..", "services/eudi-credential-issuer/Dockerfile").
				WithArgs([]string{"./eudi-credential-issuer", "migrate"}).
				WithEnvironment("ISSUER_DATABASE_URL", issuerDbURL).
				WaitForCompletion(credentialIssuerDbBootstrap)
			credentialIssuerMaterialize := eudiAdapterCli("credential-issuer-materialize",
				"./eudi-adapter", "materialize-credential-issuer",
				"--database-schema=source_registry", "--secrets-dir=/source-certificates",
				"--wallet-roots-dir=/wallet-roots", "--output-dir=/runtime-issuer", "--owner=1000").
				WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", sourceRegistryReaderURL).
				WithEnvironment("EUDI_BRI_URL", eudiBriURL).
				WithEnvironment("ISSUER_EUDI_KEY_STORAGE", m.config("Gbo:IssuerEudiKeyStorage")).
				WithEnvironment("ISSUER_EUDI_USER_AUTHENTICATION", m.config("Gbo:IssuerEudiUserAuthentication")).
				WithEnvironment("ISSUER_EUDI_PID_TRUSTED_AUTHORITIES", m.config("Gbo:IssuerEudiPidTrustedAuthorities")).
				WithEnvironment("TEST_PID_SOURCE_URL", "http://test-pid-source:8080/attestations").
				WithEnvironment("EUDI_TRUST_PUBLIC_URL", trustPublicURL).
				WithEnvironment("GBO_PID_TRUST_LIST_URL", m.config("Gbo:PidTrustListUrl")).
				WithEnvironment("GBO_LOTE_SIGNER_ROOT_FILE", "/lote-root/lote-ca-cert.pem").
				WithBindMount(secretsDir, "/source-certificates", aspire.BindMountIsReadOnly(true)).
				WithBindMount(m.configOr("Gbo:WalletRootsDir", m.repo(".local/wallet-roots")), "/wallet-roots", aspire.BindMountIsReadOnly(true)).
				WithBindMount(filepath.Join(publicCertificatesDir, "development-ca"), "/lote-root", aspire.BindMountIsReadOnly(true)).
				WithBindMount(m.configOr("Gbo:PidTrustAnchorsFile", m.repo("services/asi-wallet-verifier/pid-issuer-anchors.txt")), "/pid-trust-anchors.txt", aspire.BindMountIsReadOnly(true)).
				WithVolume("/runtime-issuer", aspire.VolumeName("gbo-credential-issuer-runtime")).
				WaitForCompletion(sourceRegistryMigrations)

			goService("test-pid-source", "Dockerfile", 8080, "../services/test-pid-source", 0).
				WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz")).
				WithEnvironment("PERSONS_FILE", "/persons/personen.json").
				WithBindMount(m.repo("services/brp-graphql-server/mockdata/personen.json"), "/persons/personen.json", aspire.BindMountIsReadOnly(true))

			m.dockerfile("eudi-credential-issuer", "..", "services/eudi-credential-issuer/Dockerfile").
				WithHttpEndpoint(aspire.HttpEndpointPort(9429), aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
				WithHttpEndpoint(aspire.HttpEndpointPort(9431), aspire.HttpEndpointTargetPort(8081), aspire.HttpEndpointName("internal"), aspire.HttpEndpointIsProxied(false)).
				WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz")).
				WithEnvironment("ISSUER_PUBLIC_URL", issuerPublicURL).
				WithEnvironment("ISSUER_CONFIG_FILE", "/config/issuer.json").
				WithEnvironment("ISSUER_DATABASE_URL", issuerDbURL).
				WithEnvironment("ISSUER_LISTEN_ADDR", ":8080").
				WithEnvironment("ISSUER_INTERNAL_ADDR", ":8081").
				WithEnvironment("ISSUER_LOGIN_SERVER", publicURL(9425)).
				WithEnvironment("ISSUER_LOGIN_URL_MAP", publicURL(9425)+"=http://asi-authorization-server:4021").
				WithVolume("/config", aspire.VolumeName("gbo-credential-issuer-runtime"), aspire.VolumeIsReadOnly(true)).
				WithBindMount(m.repo("services/brp-graphql-server/mockdata"), "/test-pid-persons", aspire.BindMountIsReadOnly(true)).
				WaitForCompletion(credentialIssuerMigrations).
				WaitForCompletion(credentialIssuerMaterialize)

			m.container("trust-host", "nginx", "1.27-alpine").
				WithHttpEndpoint(aspire.HttpEndpointPort(9430), aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
				WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz")).
				WithBindMount(m.repo("trust-host/nginx.conf"), "/etc/nginx/conf.d/default.conf", aspire.BindMountIsReadOnly(true)).
				WithBindMount(m.repo("trust-host/pages"), "/usr/share/trust-pages", aspire.BindMountIsReadOnly(true)).
				WithBindMount(trustPublishDir, "/usr/share/trust", aspire.BindMountIsReadOnly(true))
		}
	}

	// ── ASI route: ASIP, authorization server, discovery, QTSP (asi) ─────
	if on("asi") {
		asiAuthorizationServerURL := m.configOr("Gbo:AsiAuthorizationServerPublicUrl", publicURL(9425))
		asiProviderURL := m.configOr("Gbo:AsiProviderPublicUrl", publicURL(9424))
		qtspURL := m.configOr("Gbo:QtspPublicUrl", publicURL(9427))
		issuerURL := m.configOr("Gbo:IssuerPublicUrl", "http://localhost:9429")

		// The development seal certificate of the ASIP mock and the test QTSP
		// certificate, issued on the host when missing (make demo-asi).
		sealCertificate := hostGoStep("provision-asip-seal-certificate", "asi-provider",
			filepath.Join(secretsDir, "asi-provider/seal-cert.pem"),
			"provision-seal-certificate",
			"--ca-key="+filepath.Join(developmentCaDir, "issuer-ca-key.pem"),
			"--ca-cert="+filepath.Join(developmentCaDir, "issuer-ca-cert.pem"),
			"--out-dir="+filepath.Join(secretsDir, "asi-provider"))
		qtspCertificate := hostGoStep("provision-asip-qtsp-certificate", "asi-authorization-server",
			filepath.Join(secretsDir, "asi-qtsp/qtsp-cert.pem"),
			"provision-qtsp-certificate",
			"--ca-key="+filepath.Join(developmentCaDir, "reader-ca-key.pem"),
			"--ca-cert="+filepath.Join(developmentCaDir, "reader-ca-cert.pem"),
			"--out-dir="+filepath.Join(secretsDir, "asi-qtsp"))
		for _, dir := range []string{"asi-authorization-server-wallet", "asi-authorization-server-eudi-wallet", "qtsp-reference"} {
			mkdirAll(filepath.Join(secretsDir, dir))
		}

		asiIssuanceResources := m.configOr("Gbo:AsiIssuanceResources",
			issuerURL+"/belastingdienst/eudi=NTRNL-99990200=Belastingdienst,"+issuerURL+"/rvig/eudi=NTRNL-99990300=RvIG,"+issuerURL+"/demo-unsecured/eudi=NTRNL-99990900=Demo unsecured source")
		asiAuthorizationServer := m.dockerfile("asi-authorization-server", "../services/asi-authorization-server", "").
			WithHttpEndpoint(aspire.HttpEndpointPort(9425), aspire.HttpEndpointTargetPort(4021), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz")).
			WithEnvironment("AZSP_ISSUER", asiAuthorizationServerURL).
			WithEnvironment("AZSP_RESOURCES", asiProviderURL+"/brp/authsrc-api=Basisregistratie Personen (mock)").
			WithEnvironment("AZSP_ISSUANCE_RESOURCES", asiIssuanceResources).
			WithEnvironment("AZSP_TRUST_ANCHORS_FILE", "/trust/reader-ca-cert.pem").
			WithEnvironment("AZSP_ENTITLEMENT_RECHECK_INTERVAL", "1h").
			WithEnvironment("AZSP_REQUIRE_AUTHORIZATION_DETAILS", m.configOr("Gbo:AsiRequireAuthorizationDetails", "false")).
			WithEnvironment("AZSP_WALLET_VERIFIER_URL", "http://asi-wallet-verifier:8002").
			WithEnvironment("AZSP_WALLET_VERIFIER_STATUS_URL", "http://asi-wallet-verifier:8001").
			WithEnvironment("AZSP_WALLET_VERIFIER_API_KEY_FILE", "/wallet-verifier/api-key").
			WithEnvironment("AZSP_EUDI_WALLET_VERIFIER_URL", "http://eudi-wallet-verifier:8002").
			WithEnvironment("AZSP_EUDI_WALLET_VERIFIER_STATUS_URL", "http://eudi-wallet-verifier:8001").
			WithEnvironment("AZSP_EUDI_WALLET_VERIFIER_API_KEY_FILE", "/eudi-wallet-verifier/api-key").
			WithBindMount(filepath.Join(developmentCaDir, "reader-ca-cert.pem"), "/trust/reader-ca-cert.pem", aspire.BindMountIsReadOnly(true)).
			WithBindMount(filepath.Join(secretsDir, "asi-authorization-server-wallet"), "/wallet-verifier", aspire.BindMountIsReadOnly(true)).
			WithBindMount(filepath.Join(secretsDir, "asi-authorization-server-eudi-wallet"), "/eudi-wallet-verifier", aspire.BindMountIsReadOnly(true))

		asiProvider := goService("asi-provider", "services/asi-provider/Dockerfile", 4020, "", 9424).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz")).
			WithEnvironment("ASIP_PUBLIC_URL", asiProviderURL).
			WithEnvironment("ASIP_AUTHORIZATION_SERVER", asiAuthorizationServerURL).
			WithEnvironment("ASIP_AUTHORIZATION_SERVER_INTERNAL_URL", "http://asi-authorization-server:4021").
			WithEnvironment("ASIP_OUTWAY_URL", "http://asip-outway:8080").
			WithEnvironment("ASIP_SOURCE_REGISTRY_DATABASE_URL", sourceRegistryReaderURL).
			WithEnvironment("ASIP_SEAL_KEY_FILE", "/seal/seal-key.pem").
			WithEnvironment("ASIP_SEAL_CERT_FILE", "/seal/seal-cert.pem").
			WithBindMount(filepath.Join(secretsDir, "asi-provider"), "/seal", aspire.BindMountIsReadOnly(true)).
			WaitForCompletion(sealCertificate).
			WaitForCompletion(sourceRegistryMigrations).
			WaitFor(asiAuthorizationServer)
		afterFsc(asiProvider)

		asiDiscovery := m.dockerfile("asi-discovery-stub", "../services/asi-discovery-stub", "").
			WithHttpEndpoint(aspire.HttpEndpointPort(9426), aspire.HttpEndpointTargetPort(4022), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz")).
			WithEnvironment("DIP_SOURCES", "http://asi-provider:4020/brp/dataservices").
			WaitFor(asiProvider)

		m.dockerfile("qtsp-reference", "../services/qtsp-reference", "").
			WithHttpEndpoint(aspire.HttpEndpointPort(9427), aspire.HttpEndpointTargetPort(4023), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz")).
			WithEnvironment("QTSP_PUBLIC_URL", qtspURL).
			WithEnvironment("QTSP_DIP_URL", "http://asi-discovery-stub:4022").
			WithEnvironment("QTSP_URL_MAP", asiProviderURL+"=http://asi-provider:4020,"+asiAuthorizationServerURL+"=http://asi-authorization-server:4021").
			WithEnvironment("QTSP_KEY_FILE", "/qtsp/qtsp-key.pem").
			WithEnvironment("QTSP_CERT_FILE", "/qtsp/qtsp-cert.pem").
			WithEnvironment("QTSP_SEAL_TRUST_ANCHORS", "/anchors/issuer-ca-cert.pem").
			WithEnvironment("QTSP_WALLET_VERIFIER_URL", "http://asi-wallet-verifier:8002").
			WithEnvironment("QTSP_WALLET_VERIFIER_STATUS_URL", "http://asi-wallet-verifier:8001").
			WithEnvironment("QTSP_WALLET_VERIFIER_API_KEY_FILE", "/wallet-verifier/api-key").
			WithEnvironment("QTSP_EUDI_WALLET_VERIFIER_URL", "http://eudi-wallet-verifier:8002").
			WithEnvironment("QTSP_EUDI_WALLET_VERIFIER_STATUS_URL", "http://eudi-wallet-verifier:8001").
			WithEnvironment("QTSP_EUDI_WALLET_VERIFIER_API_KEY_FILE", "/eudi-wallet-verifier/api-key").
			WithEnvironment("EUDI_PUBLIC_URL", eudiPublicURL).
			WithEnvironment("EUDI_UL_BASE", eudiUlBase).
			WithEnvironment("QTSP_ISSUANCE_ADDR", ":4443").
			WithEnvironment("QTSP_ISSUANCE_TLS_KEY_FILE", "/issuance/tls-key.der.b64").
			WithEnvironment("QTSP_ISSUANCE_TLS_CERT_FILE", "/issuance/tls-cert.der.b64").
			WithEnvironment("QTSP_ISSUANCE_CALLBACK_PATH_FILE", "/issuance/callback-path").
			WithEnvironment("QTSP_ISSUER_OFFERS_URL", m.config("Gbo:QtspIssuerOffersUrl")).
			WithEnvironment("QTSP_ISSUER_PROFILE", "wallet").
			WithEnvironment("QTSP_ISSUER_CONFIGURATION", "qtsp-reference-attestation").
			WithBindMount(filepath.Join(secretsDir, "asi-qtsp"), "/qtsp", aspire.BindMountIsReadOnly(true)).
			WithBindMount(developmentCaDir, "/anchors", aspire.BindMountIsReadOnly(true)).
			WithBindMount(filepath.Join(secretsDir, "asi-authorization-server-wallet"), "/wallet-verifier", aspire.BindMountIsReadOnly(true)).
			WithBindMount(filepath.Join(secretsDir, "asi-authorization-server-eudi-wallet"), "/eudi-wallet-verifier", aspire.BindMountIsReadOnly(true)).
			WithBindMount(filepath.Join(secretsDir, "qtsp-reference"), "/issuance", aspire.BindMountIsReadOnly(true)).
			WaitForCompletion(qtspCertificate).
			WaitFor(asiDiscovery)

		// The wallet verifiers run once they are provisioned (README, ASI
		// section); without them the authorization server offers DigiD
		// (mock) only.
		nlWalletVerifierConfig := filepath.Join(secretsDir, "asi-wallet-verifier/verification_server.toml")
		if fileExists(nlWalletVerifierConfig) {
			verifierImage := m.executable("asi-wallet-verifier-image", "bash", m.repoRoot,
				"aspire/scripts/nlwallet-image.sh", "gbo/asi-wallet-verifier:dev", "services/asi-wallet-verifier/Dockerfile")
			m.container("asi-wallet-verifier", "gbo/asi-wallet-verifier", "dev").
				WithImagePullPolicy(aspire.ImagePullPolicyNever).
				WithHttpEndpoint(aspire.HttpEndpointPort(9428), aspire.HttpEndpointTargetPort(8001), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
				WithBindMount(nlWalletVerifierConfig, "/verification_server.toml", aspire.BindMountIsReadOnly(true)).
				WaitForCompletion(verifierImage)
		}
		eudiWalletVerifierDir := filepath.Join(secretsDir, "asi-eudi-wallet-verifier")
		if fileExists(filepath.Join(eudiWalletVerifierDir, "config.json")) {
			// Copies the provisioned files for the verifier's user (1000).
			verifierRuntime := m.dockerfile("eudi-wallet-verifier-runtime", "..", "services/eudi-adapter/Dockerfile").
				WithArgs([]string{"./eudi-adapter", "project-runtime", "--from=/provisioned", "--to=/runtime", "--owner=1000"}).
				WithContainerRuntimeArgs([]string{"--network", "none"}).
				WithBindMount(eudiWalletVerifierDir, "/provisioned", aspire.BindMountIsReadOnly(true)).
				WithVolume("/runtime", aspire.VolumeName("gbo-eudi-wallet-verifier-runtime"))
			m.dockerfile("eudi-wallet-verifier", "..", "services/eudi-wallet-verifier/Dockerfile").
				WithHttpEndpoint(aspire.HttpEndpointPort(9432), aspire.HttpEndpointTargetPort(8001), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
				WithEnvironment("EUDI_WALLET_VERIFIER_CONFIG", "/config/config.json").
				WithVolume("/config", aspire.VolumeName("gbo-eudi-wallet-verifier-runtime"), aspire.VolumeIsReadOnly(true)).
				WaitForCompletion(verifierRuntime)
		}
	}

	// ── OpenFTV Manager owning policy distribution (manager) ─────────────
	if on("manager") {
		keycloakAdminPassword := m.generated("keycloak-admin-password")
		auditorPassword := m.generated("ftv-manager-auditor-password")
		deployPassword := m.generated("ftv-manager-deploy-password")
		oidcAuthority := m.configOr("Gbo:FtvOidcAuthority", publicURL(9284)+"/realms/gbo")
		managerURL := m.configOr("Gbo:FtvManagerUrl", publicURL(9280))

		keycloak := m.container("keycloak", "quay.io/keycloak/keycloak", "26.0.7").
			WithHttpEndpoint(aspire.HttpEndpointPort(9284), aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/realms/gbo")).
			WithArgs([]string{"start-dev", "--import-realm", "--http-port=8080"}).
			WithEnvironment("KC_BOOTSTRAP_ADMIN_USERNAME", "admin").
			WithEnvironment("KC_BOOTSTRAP_ADMIN_PASSWORD", keycloakAdminPassword).
			WithEnvironment("FTV_MANAGER_AUDITOR_PASSWORD", auditorPassword).
			WithEnvironment("FTV_MANAGER_DEPLOY_PASSWORD", deployPassword).
			WithEnvironment("KC_HOSTNAME_STRICT", "false").
			WithEnvironment("KC_HTTP_ENABLED", "true").
			WithEnvironment("KC_PROXY_HEADERS", "xforwarded").
			WithBindMount(m.repo("services/openftv-manager-ui/keycloak/realm-gbo.json"), "/opt/keycloak/data/import/realm-gbo.json", aspire.BindMountIsReadOnly(true))

		// The OpenFTV version must match the PDP's OPENFTV_REF; CI checks it.
		openftvManager := m.container("openftv-manager", "registry.gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/manager", "v0.1.0").
			WithHttpEndpoint(aspire.HttpEndpointPort(9280), aspire.HttpEndpointTargetPort(8443), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithHttpEndpoint(aspire.HttpEndpointPort(9281), aspire.HttpEndpointTargetPort(9443), aspire.HttpEndpointName("internal"), aspire.HttpEndpointIsProxied(false)).
			WithHttpEndpoint(aspire.HttpEndpointPort(9282), aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("health"), aspire.HttpEndpointIsProxied(false)).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/healthz"), aspire.HttpHealthCheckEndpointName("health")).
			WithEnvironment("MANAGER_DATABASE_URL", aspire.RefExpr("postgres://ftv:%v@postgres-ftv:5432/ftv_manager?sslmode=disable", ftvPostgresPassword)).
			WithEnvironment("MANAGER_POLICIES_STORE", "/authz").
			WithEnvironment("MANAGER_POLICIES_STORE_RECURSE", "true").
			WithEnvironment("MANAGER_AUTHORIZATION_AUTHENTICATE", "true").
			WithEnvironment("MANAGER_AUTHORIZATION_FAIL_CLOSED_ON_EMPTY", "true").
			WithEnvironment("MANAGER_TAGS_PATH", "/tags").
			WithEnvironment("MANAGER_BUNDLE_CONFIGS", "/bundles").
			WithEnvironment("MANAGER_INTERNAL_PORT", "9443").
			WithEnvironment("MANAGER_LOG_LEVEL", "info").
			WithEnvironment("MANAGER_OIDC_ISSUER", oidcAuthority).
			WithEnvironment("MANAGER_OIDC_JWKS_URL", "http://keycloak:8080/realms/gbo/protocol/openid-connect/certs").
			WithEnvironment("MANAGER_OIDC_AUDIENCE", "openftv-manager-api").
			WithEnvironment("MANAGER_OIDC_ROLES_CLAIM", "roles").
			WithEnvironment("MANAGER_ADL_PG_URL", aspire.RefExpr("postgres://ftv:%v@postgres-ftv:5432/ftv_adl?sslmode=disable", ftvPostgresPassword)).
			WithBindMount(m.repo("services/openftv-manager/authz"), "/authz", aspire.BindMountIsReadOnly(true)).
			WithBindMount(m.repo("services/openftv-manager/bundles"), "/bundles", aspire.BindMountIsReadOnly(true)).
			WithBindMount(m.repo("services/openftv-manager/tags"), "/tags", aspire.BindMountIsReadOnly(true)).
			WaitForCompletion(postgresFtvInit).
			WaitFor(keycloak)

		m.container("openftv-manager-ui", "registry.gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/management-interface", "v0.1.0").
			WithHttpEndpoint(aspire.HttpEndpointPort(9283), aspire.HttpEndpointTargetPort(8080), aspire.HttpEndpointName("http"), aspire.HttpEndpointIsProxied(false)).
			WithEnvironment("VITE_PAP_BASE_URL", managerURL).
			WithEnvironment("VITE_PIP_BASE_URL", managerURL).
			WithEnvironment("VITE_OIDC_AUTHORITY", oidcAuthority).
			WithEnvironment("VITE_OIDC_CLIENT_ID", "openftv-manager-ui").
			WithEnvironment("MANAGER_MAX_LOGO_SIZE", "102400").
			WaitFor(openftvManager)

		// Pushes policies/ into the Manager; the PDP then starts and pulls
		// the bundle (make demo-manager / manager-seed). Rerun from the
		// dashboard after editing policies, then restart openftv-pdp.
		managerSeed := m.executable("manager-seed", "python3", m.repoRoot,
			"scripts/seed-openftv-manager.py", "--url", "http://localhost:9280").
			WithEnvironment("FTV_MANAGER_DEPLOY_PASSWORD", deployPassword).
			WaitForCompletion(policyCheck).
			WaitFor(openftvManager)
		openftvPdp.WaitForCompletion(managerSeed)
	}

	// ── Public tunnel for the wallet (Gbo:CloudflareTunnel) ──────────────
	// The overlay docker-compose.cloudflare-tunnel.yml was: cloudflared with
	// a tunnel token, routing the public EUDI, ASI and issuer hostnames here.
	if strings.EqualFold(m.config("Gbo:CloudflareTunnel"), "true") {
		tunnelToken := b.AddParameter("cloudflare-tunnel-token", aspire.AddParameterSecret(true))
		tunnel := m.containerArgs(m.container("eudi-tunnel", "cloudflare/cloudflared", "latest"),
			"tunnel", "--no-autoupdate", "run", "--token", tunnelToken)
		for _, name := range []string{"eudi-issuance-server", "eudi-adapter", "asi-wallet-verifier", "eudi-wallet-verifier", "asi-authorization-server", "eudi-credential-issuer", "trust-host"} {
			if r, ok := m.resources[name]; ok {
				tunnel.WaitFor(r.handle)
			}
		}
	}

	// ── Frontends: Vite dev servers on the host ──────────────────────────
	// Fixed host ports: the frontends link to each other, the consent
	// redirect derives the portal's port as the consumer mock's port + 1, and
	// the landing page shows the URLs.
	const landingPort, dvMockPort, devPortalPort, keyperPort = 9000, 9001, 9003, 9004
	const consentPortalPort = dvMockPort + 1
	allowedHosts := publicHost
	if extra := m.config("Gbo:ExtraAllowedHosts"); extra != "" {
		allowedHosts += "," + extra
	}

	// The installers run `npm ci`, which needs npm 11 (Node.js 24 or newer,
	// as in CI and the frontend images): npm 10 reads the lockfiles as out
	// of sync. Aspire's installer runs the first `npm` on the PATH that
	// `aspire run` was started with, so that one has to be new enough; the
	// AppHost stops with a hint when it is not.
	const minimumNodeMajor = 24
	if m.runMode {
		checkNodeOnPath(minimumNodeMajor)
	}

	frontend := func(name string, port int) aspire.ViteAppResource {
		app := b.AddViteApp(name, "../"+name).
			// npm ci: install exactly the lockfile, never rewrite it.
			WithNpm(aspire.NpmInstall(true), aspire.NpmInstallCommand("ci")).
			// Not proxied: the dev server itself listens on the fixed port, so
			// the links between the frontends (and the consent redirect to the
			// consumer mock port + 1) hit the app, not a proxy in front of it.
			WithEndpointCallback("http", func(e aspire.EndpointUpdateContext) {
				p, proxied := port, false
				e.SetPort(&p)
				e.SetTargetPort(&p)
				e.SetIsProxied(&proxied)
			}).
			WithEnvironment("__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS", allowedHosts)
		m.register(name, app, ownBuild,
			func(p aspire.Resource) { app.WithParentRelationship(p) },
			func(tags []string, order int) { app.WithTags(tags).WithDisplayOrder(order) })
		return app
	}

	developerPortal := frontend("developer-portal", devPortalPort).
		WithEnvironment("DEV_API_TARGET", devPortalBackend.GetEndpoint("http")).
		WithEnvironment("BD_GRAPHQL_TARGET", graphqlServer.GetEndpoint("http")).
		WithEnvironment("BRP_GRAPHQL_TARGET", brpGraphqlServer.GetEndpoint("http")).
		// Trace links open the dashboard; it has no log search by trace id,
		// so the Grafana link is left out.
		WithEnvironment("VITE_JAEGER_PUBLIC_URL", dashboardPublicURL+"/traces/detail/{traceId}").
		WithEnvironment("VITE_GRAFANA_PUBLIC_URL", "").
		WithEnvironment("VITE_EUDI_UL_BASE", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance").
		WaitFor(devPortalBackend)
	// The portal proxies to whichever backends the profiles start.
	for _, t := range []struct {
		variable string
		target   aspire.ContainerResource
	}{
		{"PORTAL_API_TARGET", consentPortalBackend},
		{"DVTP_API_TARGET", dienstverlenerBackend},
		{"IR_API_TARGET", irBackend},
		{"LVG_GRAPHQL_TARGET", lvgGraphqlServer},
	} {
		if t.target != nil {
			developerPortal.WithEnvironment(t.variable, t.target.GetEndpoint("http"))
		}
	}

	dvtpAvailable := "false"
	if on("dvtp") {
		dvtpAvailable = "true"
	}
	landingPage := frontend("landing-page", landingPort).
		WithEnvironment("VITE_DIENSTVERLENER_URL", publicURL(dvMockPort)).
		WithEnvironment("VITE_INSTALLATIE_REGISTER_URL", publicURL(keyperPort)).
		WithEnvironment("VITE_TOESTEMMINGSPORTAAL_URL", publicURL(consentPortalPort)).
		WithEnvironment("VITE_DEVELOPER_PORTAL_URL", publicURL(devPortalPort)).
		WithEnvironment("VITE_EUDI_UL_BASE", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance").
		WithEnvironment("VITE_JAEGER_PUBLIC_URL", dashboardPublicURL+"/traces").
		WithEnvironment("VITE_GRAFANA_PUBLIC_URL", dashboardPublicURL+"/structuredlogs").
		WithEnvironment("VITE_FSC_CONTROLLER_BRON_URL", publicURL(8095)).
		WithEnvironment("VITE_FSC_CONTROLLER_DVTP_URL", publicURL(8096)).
		WithEnvironment("VITE_FSC_CONTROLLER_EUDI_URL", publicURL(8094)).
		WithEnvironment("VITE_VERSION_STRING", m.config("Gbo:VersionString")).
		// Without the dvtp profile the consumer mock, consent portal and
		// Installatie Register do not run; the page then shows their start
		// buttons as unavailable instead of linking to a dead port.
		WithEnvironment("VITE_DVTP_AVAILABLE", dvtpAvailable)

	if on("dvtp") {
		frontend("dienstverlener-mock", dvMockPort).
			WithEnvironment("DV_API_TARGET", dienstverlenerBackend.GetEndpoint("http")).
			WithEnvironment("VITE_CONSUMER_PEER_ID", "99999999900000000300").
			WaitFor(dienstverlenerBackend)
		frontend("toestemmingsportaal-frontend", consentPortalPort).
			WithEnvironment("PORTAL_API_TARGET", consentPortalBackend.GetEndpoint("http")).
			WithEnvironment("VITE_ALLOWED_RETURN_ORIGINS", publicURL(dvMockPort)+","+publicURL(keyperPort)).
			WaitFor(consentPortalBackend)
		frontend("keyper-mock", keyperPort).
			WithEnvironment("IR_API_TARGET", irBackend.GetEndpoint("http")).
			WithEnvironment("VITE_CONSUMER_PEER_ID", "99999999900000001000").
			WithEnvironment("VITE_CONSENT_PORTAL_URL", publicURL(consentPortalPort)).
			WaitFor(irBackend)
	}

	// The EUDI adapter and the public EUDI URL, when the eudi profile runs.
	if eudiAdapter != nil {
		developerPortal.
			WithEnvironment("EUDI_API_TARGET", eudiAdapter.GetEndpoint("http")).
			WithEnvironment("VITE_EUDI_PUBLIC_URL", eudiPublicURL)
		landingPage.
			WithEnvironment("EUDI_API_TARGET", eudiAdapter.GetEndpoint("http")).
			WithEnvironment("VITE_EUDI_PUBLIC_URL", eudiPublicURL).
			WaitFor(eudiAdapter)
	}
	if ownIssuer := m.config("Gbo:IssuerPublicUrl"); ownIssuer != "" {
		developerPortal.WithEnvironment("VITE_OWN_ISSUER_PUBLIC_URL", ownIssuer)
	}

	// ── Grouping in the dashboard ────────────────────────────────────────
	// Visual only: a child is listed under its parent in the dashboard and
	// keeps its own lifecycle, waits and ports. Each FSC peer folds under its
	// Controller, the run-once helpers under the resource they prepare.
	for _, peer := range []string{"directory", "edi", "bd", "hv", "cr", "pdp", "ir", "brp", "int", "asip"} {
		for _, part := range []string{"certs", "migrations-controller", "migrations-manager", "migrations-txlog-api", "manager", "txlog-api", "inway", "outway"} {
			m.nestUnder(peer+"-"+part, peer+"-controller")
		}
	}
	m.nestUnder("directory-ui", "directory-controller")
	for _, name := range m.seeds {
		m.nestUnder(name, "directory-controller")
	}
	m.nestUnder("fsc-databases", "fsc-postgres")
	pkiParent := "openftv-pdp"
	if _, ok := m.resources["certportal"]; ok {
		pkiParent = "certportal"
	}
	for _, cp := range [][2]string{
		{"cfssl", "certportal"}, {"fsc-root-ca", "certportal"},
		{"pki-tools-image", pkiParent},
		{"postgres-ftv-init", "postgres-ftv"},
		{"pdp-cert", "openftv-pdp"}, {"policy-check", "openftv-pdp"}, {"pdp-admission", "openftv-pdp"},
		{"source-registry-bootstrap", "postgres-eudi"}, {"source-registry-migrations", "postgres-eudi"},
		{"credential-issuer-db-bootstrap", "postgres-eudi"},
		{"provision-belastingdienst-certificates", "source-reconciler"}, {"provision-rvig-certificates", "source-reconciler"},
		{"provision-demo-unsecured-certificates", "source-reconciler"},
		{"eudi-issuance-server-image", "eudi-issuance-server"}, {"eudi-migrations", "eudi-issuance-server"},
		{"eudi-issuance-materialize", "eudi-issuance-server"}, {"provision-qtsp-issuance-certificates", "eudi-issuance-server"},
		{"credential-issuer-migrations", "eudi-credential-issuer"}, {"credential-issuer-materialize", "eudi-credential-issuer"},
		{"eudi-wallet-verifier-runtime", "eudi-wallet-verifier"}, {"asi-wallet-verifier-image", "asi-wallet-verifier"},
		{"provision-asip-seal-certificate", "asi-provider"}, {"provision-asip-qtsp-certificate", "qtsp-reference"},
		{"manager-seed", "openftv-manager"}, {"openftv-manager-ui", "openftv-manager"},
	} {
		m.nestUnder(cp[0], cp[1])
	}

	// ── Groups in the dashboard ──────────────────────────────────────────
	// Every resource carries one tag, and the groups sort in this order:
	// "own build" (the services and frontends built from this repository),
	// "off the shelf" (published components: Postgres, OpenFSC, cfssl,
	// Keycloak, the OpenFTV Manager, nginx, cloudflared, and the nl-wallet
	// issuance server and verifier), "monitoring" (the collector) and "setup"
	// (run-once steps). A resource gets its group from how it is added
	// (Dockerfile: own build, image: off the shelf, host process: setup);
	// the exceptions are listed here.
	m.classify("otel-collector", monitoring)
	// Released OpenFTV built here with the GBO mapper patch
	// (services/openftv-pdp/Dockerfile): both groups, sorted with the own
	// builds.
	m.alsoTag("openftv-pdp", offTheShelf)
	for name := range m.resources {
		if strings.HasPrefix(name, "fsc-seed-") || strings.Contains(name, "-migrations") ||
			strings.HasSuffix(name, "-bootstrap") || strings.HasSuffix(name, "-materialize") {
			m.classify(name, setup)
		}
	}
	for _, name := range []string{"postgres-ftv-init", "pdp-admission", "fsc-databases", "source-reconciler", "eudi-wallet-verifier-runtime"} {
		m.classify(name, setup)
	}
	m.labelAll()

	// ── Grouping in Docker Desktop and OrbStack ──────────────────────────
	// Both group containers by the compose project label. Every container the
	// AppHost starts carries it, with its resource name as the service. The
	// project name is its own, so `docker compose -p gbo-demo down` cannot
	// touch these containers.
	const composeProject = "gbo-demo-aspire"
	for _, c := range m.containers {
		c.handle.WithContainerRuntimeArgs([]string{
			"--label", "com.docker.compose.project=" + composeProject,
			"--label", "com.docker.compose.service=" + c.name,
		})
	}
}

// randomHex returns n random bytes, hex encoded.
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(buf)
}
