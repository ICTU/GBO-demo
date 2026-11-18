#:package Aspire.Hosting.AppHost@17.0.0
#:package Aspire.Hosting.Dotnet@17.0.0
#:package Aspire.Hosting.JavaScript@17.0.0

// GBO-demo AppHost. Run it from this directory with `aspire run`; see
// README.md. PLAN.md tracks what the migration still has to bring over.
//
// The Go services run as containers built from their own Dockerfiles, with
// the same hostnames, container ports and mounts as docker-compose.yml. The
// FSC seeds, the PIP pull config and the collector address them by name, and
// Aspire's container network resolves resource names the same way compose
// does.

using System.Security.Cryptography;

var builder = DistributedApplication.CreateBuilder(args);

var repoRoot = Path.GetFullPath(Path.Combine(builder.AppHostDirectory, ".."));
string Repo(string relative) => Path.Combine(repoRoot, relative);

// ── Secrets and settings ─────────────────────────────────────────────────
// Required secrets have no default: set them once with
//   aspire secret set Parameters:ftv-postgres-password <value>
// or the dashboard prompts for them. The LDV values default to the same
// local-demo values as docker-compose.yml.
var ftvPostgresPassword = builder.AddParameter("ftv-postgres-password", secret: true);
var ftvAdlReaderPassword = builder.AddParameter("ftv-adl-reader-password", secret: true);
var ldvWriteToken = builder.AddParameter("ldv-write-token", "local-demo-logboek-token", secret: true);
var ldvReadToken = builder.AddParameter("ldv-read-token", "local-demo-logboek-read-token", secret: true);
var pseudonymKeyBd = builder.AddParameter("ldv-subject-pseudonym-key", "local-demo-pseudonym-key", secret: true);
var pseudonymKeyBrp = builder.AddParameter("ldv-subject-pseudonym-key-brp", "local-demo-pseudonym-key-brp", secret: true);
var pseudonymKeyGbo = builder.AddParameter("ldv-subject-pseudonym-key-gbo", "local-demo-pseudonym-key-gbo", secret: true);
var bsnAuthorisedOins = builder.AddParameter("bsn-authorised-oins", "99999999900000000200");

// ── Telemetry ────────────────────────────────────────────────────────────
// The services export to the collector, as in compose; the collector
// forwards to the dashboard with the dashboard's OTLP API key. The key is
// fixed here so the collector can be given it: AppHost:OtlpApiKey from
// configuration, or a fresh random one per run.
if (string.IsNullOrEmpty(builder.Configuration["AppHost:OtlpApiKey"]))
{
    builder.Configuration["AppHost:OtlpApiKey"] = Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
}
var dashboardOtlp = new Uri(builder.Configuration["ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL"]
    ?? throw new InvalidOperationException("ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL is not set; start the AppHost with `aspire run`."));
if (dashboardOtlp.Scheme != "http")
{
    throw new InvalidOperationException("The collector forwards to the dashboard over plaintext gRPC; use the http launch profile.");
}

var jaeger = builder.AddContainer("jaeger", "jaegertracing/all-in-one", "1.54")
    .WithContainerRuntimeArgs("--user", "0")
    .WithHttpEndpoint(targetPort: 16686, name: "http")
    .WithEndpoint(targetPort: 4317, name: "otlp-grpc", scheme: "http")
    .WithHttpHealthCheck("/")
    .WithEnvironment("COLLECTOR_OTLP_ENABLED", "true")
    .WithEnvironment("SPAN_STORAGE_TYPE", "badger")
    .WithEnvironment("BADGER_EPHEMERAL", "false")
    .WithEnvironment("BADGER_DIRECTORY_KEY", "/badger/keys")
    .WithEnvironment("BADGER_DIRECTORY_VALUE", "/badger/values")
    .WithEnvironment("BADGER_SPAN_STORE_TTL", "168h")
    .WithVolume("gbo-jaeger-badger", "/badger");

var otelCollector = builder.AddContainer("otel-collector", "otel/opentelemetry-collector-contrib", "0.96.0")
    .WithBindMount(Repo("aspire/otel-collector.yaml"), "/etc/otelcol-contrib/config.yaml", isReadOnly: true)
    .WithEndpoint(targetPort: 4317, name: "otlp-grpc", scheme: "http")
    .WithEndpoint(targetPort: 4318, name: "otlp-http", scheme: "http")
    .WithHttpEndpoint(targetPort: 13133, name: "health")
    .WithHttpHealthCheck("/", endpointName: "health")
    .WithContainerRuntimeArgs("--add-host", "host.docker.internal:host-gateway")
    .WithEnvironment("ASPIRE_DASHBOARD_OTLP_GRPC", "host.docker.internal:" + dashboardOtlp.Port)
    .WithEnvironment("ASPIRE_DASHBOARD_OTLP_API_KEY", builder.Configuration["AppHost:OtlpApiKey"]!)
    .WaitFor(jaeger);

// A service built from its Dockerfile, exporting spans to the collector.
IResourceBuilder<ContainerResource> GoService(string name, string dockerfile, int port, string? context = null)
{
    return builder.AddDockerfile(name, context ?? "..", dockerfile)
        .WithHttpEndpoint(targetPort: port, name: "http")
        .WithEnvironment("OTEL_SERVICE_NAME", name)
        .WithEnvironment("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4317")
        .WaitFor(otelCollector);
}

// ── Logboek Dataverwerkingen: one per Verantwoordelijke ──────────────────
var logboeken = new Dictionary<string, IResourceBuilder<ContainerResource>>();
foreach (var (name, register, logbookId) in new[]
{
    ("logboek-bd", "bd", "https://logboek.belastingdienst.nl/data-processing-operations"),
    ("logboek-brp", "brp", "https://logboek.rvig.nl/data-processing-operations"),
    ("logboek-toestemming", "toestemming", "https://logboek.gbo.overheid.nl/toestemming/data-processing-operations"),
    ("logboek-eudi-adapter", "eudi-adapter", "https://logboek.gbo.overheid.nl/eudi-adapter/data-processing-operations"),
    ("logboek-afnemer", "afnemer", "https://logboek.hypotheek-bv.test/data-processing-operations"),
    ("logboek-lvg", "lvg", "https://logboek.lvg.test/data-processing-operations"),
    ("logboek-ir", "ir", "https://logboek.installatieregister.test/data-processing-operations"),
})
{
    logboeken[name] = GoService(name, "services/ldv-logboek/Dockerfile", 4016)
        .WithHttpHealthCheck("/health")
        .WithEnvironment("DATABASE_PATH", "/data/logboek.db")
        .WithEnvironment("REGISTER_PATH", $"/config/verwerkingsactiviteiten-{register}.json")
        .WithEnvironment("LDV_LOGBOOK_ID", logbookId)
        .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
        .WithEnvironment("LDV_READ_TOKEN", ldvReadToken)
        .WithVolume($"gbo-{name}-data", "/data");
}

// ── OpenFTV: decision log database and PDP ───────────────────────────────
var postgresFtv = builder.AddContainer("postgres-ftv", "postgres", "16-alpine")
    .WithEndpoint(targetPort: 5432, name: "tcp", scheme: "tcp")
    .WithEnvironment("POSTGRES_USER", "ftv")
    .WithEnvironment("POSTGRES_DB", "ftv_manager")
    .WithEnvironment("POSTGRES_PASSWORD", ftvPostgresPassword)
    .WithBindMount(Repo("services/openftv-manager/initdb"), "/docker-entrypoint-initdb.d", isReadOnly: true)
    .WithVolume("gbo-postgres-ftv-data", "/var/lib/postgresql/data");

// Creates the SELECT-only role the developer portal reads the ADL with.
// Waits for the server itself: a container has no health check here.
var postgresFtvInit = builder.AddContainer("postgres-ftv-init", "postgres", "16-alpine")
    .WithEnvironment("PGHOST", "postgres-ftv")
    .WithEnvironment("PGUSER", "ftv")
    .WithEnvironment("PGPASSWORD", ftvPostgresPassword)
    .WithEnvironment("PGDATABASE", "ftv_adl")
    .WithEnvironment("ADL_READER_PASSWORD", ftvAdlReaderPassword)
    .WithBindMount(Repo("services/openftv-manager/adl/adl-reader.sql"), "/init/adl-reader.sql", isReadOnly: true)
    .WithEntrypoint("sh")
    .WithArgs("-c", """
        for i in $(seq 1 60); do psql -c 'select 1' >/dev/null 2>&1 && break; sleep 1; done
        psql -v ON_ERROR_STOP=1 -v reader_password="$ADL_READER_PASSWORD" -f /init/adl-reader.sql
        """)
    .WaitFor(postgresFtv);

var dvtpOnboardingRegister = builder.AddDockerfile("dvtp-onboarding-register", "../services/dvtp-onboarding-register")
    .WithHttpEndpoint(targetPort: 4015, name: "http")
    .WithHttpHealthCheck("/health")
    .WithEnvironment("DATABASE_PATH", "/data/onboarding.db")
    .WithEnvironment("SEED_DEMO_DATA", "true")
    .WithVolume("gbo-dvtp-onboarding-data", "/data");

var consentRegister = GoService("consent-register", "services/consent-register/Dockerfile", 4002)
    .WithHttpHealthCheck("/health")
    .WithEnvironment("CONSENT_STORE", "memory")
    .WithEnvironment("CONSENT_TOKEN_ISSUER", "https://consent-register.gbo.test")
    .WithEnvironment("CONSENT_TOKEN_AUDIENCE", "gbo:dvtp:pdp")
    .WithEnvironment("STATUS_PORT", "4012")
    .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-toestemming:4016")
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyGbo)
    .WaitFor(logboeken["logboek-toestemming"]);

// The PDP's TLS certificate (SAN openftv-pdp), made once by the FSC PKI
// script in the pki-tools image. Skipped when it exists.
var pkiToolsImage = builder.AddExecutable("pki-tools-image", "docker", Repo("fsc-infra/pki"),
    "build", "-q", "-f", "Dockerfile.pki", "-t", "gbo-demo/pki-tools:local", ".");
var pdpCert = builder.AddExecutable("pdp-cert", "bash", repoRoot, "fsc-infra/pki/generate-pdp-cert.sh")
    .WaitForCompletion(pkiToolsImage);

var openftvPdp = builder.AddDockerfile("openftv-pdp", "..", "services/openftv-pdp/Dockerfile")
    .WithHttpsEndpoint(targetPort: 8443, name: "https")
    .WithHttpEndpoint(targetPort: 8080, name: "health")
    .WithHttpHealthCheck("/healthz", endpointName: "health")
    .WithEnvironment("PDP_POLICIES_LANGUAGE", "rego")
    .WithEnvironment("PDP_POLICIES_STORE", "/policies")
    .WithEnvironment("PDP_POLICIES_STORE_RECURSE", "true")
    .WithEnvironment("PDP_ADL_PG_URL", ReferenceExpression.Create($"postgres://ftv:{ftvPostgresPassword}@postgres-ftv:5432/ftv_adl?sslmode=disable"))
    .WithEnvironment("PDP_ADL_SERVICE_NAME", "gbo")
    .WithEnvironment("PDP_LOG_LEVEL", "info")
    .WithEnvironment("PDP_REQUEST_MAPPINGS", "ftvgraphql")
    .WithEnvironment("PDP_PIP_PULL_CONFIGS", "/pip-pull/dvtp-onboarding.yaml")
    .WithEnvironment("GBO_CONSENT_URL", "http://consent-register:4002")
    .WithEnvironment("GBO_CONSENT_STATUS_URL", "http://pdp-outway:8080/consent-status")
    .WithEnvironment("GBO_CONSENT_ISSUER", "https://consent-register.gbo.test")
    .WithEnvironment("GBO_CONSENT_AUDIENCE", "gbo:dvtp:pdp")
    .WithEnvironment("PDP_BUNDLE_MANAGER", "")
    .WithEnvironment("PDP_TLS_CERT", "/certs/pdp-service.pem")
    .WithEnvironment("PDP_TLS_KEY", "/certs/pdp-service-key.pem")
    .WithBindMount(Repo("policies"), "/policies", isReadOnly: true)
    .WithBindMount(Repo("schemas/pdp-mirror"), "/etc/gbo/ftv-graphql", isReadOnly: true)
    .WithBindMount(Repo("services/openftv-pdp/certs"), "/certs", isReadOnly: true)
    .WithBindMount(Repo("services/openftv-pdp/pip-pull"), "/pip-pull", isReadOnly: true)
    .WaitFor(postgresFtv)
    .WaitForCompletion(postgresFtvInit)
    .WaitForCompletion(pdpCert)
    .WaitFor(consentRegister)
    .WaitFor(dvtpOnboardingRegister);

// ── Sources: Belastingdienst, RvIG and an unsecured demo source ──────────
var bsnkMock = GoService("bsnk-mock", "Dockerfile", 4003, context: "../services/bsnk-mock")
    .WithHttpHealthCheck("/health")
    .WithEnvironment("BSN_AUTHORISED_OINS", bsnAuthorisedOins);

var decryptionComponent = GoService("decryption-component", "Dockerfile", 4003, context: "../services/bsnk-mock")
    .WithHttpHealthCheck("/health")
    .WithEnvironment("DECRYPTION_COMPONENT_ONLY", "true");

var graphqlServer = GoService("graphql-server", "services/graphql-server/Dockerfile", 4000)
    .WithHttpHealthCheck("/health")
    .WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata.json")
    .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-bd:4016")
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyBd)
    .WithEnvironment("LDV_YEAR_ACTIVITY_TEMPLATE", "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-%d/v1")
    .WithBindMount(Repo("services/graphql-server/config"), "/config", isReadOnly: true)
    .WithVolume("gbo-ldv-outbox-graphql-server", "/data")
    .WaitFor(logboeken["logboek-bd"]);

var brpGraphqlServer = GoService("brp-graphql-server", "services/brp-graphql-server/Dockerfile", 4001)
    .WithHttpHealthCheck("/health")
    .WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata.json")
    .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-brp:4016")
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyBrp)
    .WithBindMount(Repo("services/brp-graphql-server/config"), "/config", isReadOnly: true)
    .WithVolume("gbo-ldv-outbox-brp-graphql-server", "/data")
    .WaitFor(logboeken["logboek-brp"]);

GoService("unsecured-graphql-server", "services/graphql-server/Dockerfile", 4000)
    .WithHttpHealthCheck("/health")
    .WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata-unsecured.json")
    .WithBindMount(Repo("services/graphql-server/config"), "/config", isReadOnly: true);

// The source-side sidecar in front of a source: decrypts the polymorphic
// identity and writes the source's logbook records.
IResourceBuilder<ContainerResource> Sidecar(string name, string upstream, string logbook,
    IResourceBuilder<ParameterResource> pseudonymKey, string activityBase, string activityPrefix,
    IResourceBuilder<ContainerResource> source)
{
    return GoService(name, "services/bron-sidecar/Dockerfile", 4011)
        .WithHttpHealthCheck("/health")
        .WithEnvironment("UPSTREAM_URL", upstream)
        .WithEnvironment("DECRYPTION_URL", "http://decryption-component:4003")
        .WithEnvironment("SUBJECT_KEYS_DIR", "/app/config/demo-keys")
        .WithEnvironment("OWN_PEER_OIN", "99999999900000000200")
        .WithEnvironment("PSEUDONYM_VARS", "bsn")
        .WithEnvironment("LDV_LOGBOOK_URL", $"http://{logbook}:4016")
        .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
        .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKey)
        .WithEnvironment("LDV_DECRYPTION_ACTIVITY", $"{activityBase}/{activityPrefix}-identiteit-ontsleuteling/v1")
        .WithEnvironment("LDV_FORWARD_ACTIVITY", $"{activityBase}/{activityPrefix}-bronquery-doorgifte/v1")
        .WithVolume($"gbo-ldv-outbox-{name}", "/data")
        .WaitFor(source)
        .WaitFor(decryptionComponent)
        .WaitFor(logboeken[logbook]);
}

Sidecar("bron-sidecar", "http://graphql-server:4000", "logboek-bd", pseudonymKeyBd,
    "https://logboek.belastingdienst.nl/verwerkingsactiviteiten", "bd", graphqlServer);
Sidecar("brp-sidecar", "http://brp-graphql-server:4001", "logboek-brp", pseudonymKeyBrp,
    "https://logboek.rvig.nl/verwerkingsactiviteiten", "brp", brpGraphqlServer);

// ── Developer portal ─────────────────────────────────────────────────────
// The FSC transaction-log certificates are mounted as in compose; until
// fsc-infra runs under the AppHost (phase 2) the directories may be empty.
foreach (var org in new[] { "edi-issuer", "hypotheekverlener-mock", "installatieregister", "asip", "belastingdienst-mock" })
{
    Directory.CreateDirectory(Repo($"fsc-infra/orgs/{org}/pki/internal"));
}

var ldvLogbooks = string.Join(",",
    "https://logboek.hypotheek-bv.test/data-processing-operations=Hypotheek-BV=http://logboek-afnemer:4016=start",
    "https://logboek.gbo.overheid.nl/eudi-adapter/data-processing-operations=GBO EUDI-adapter=http://logboek-eudi-adapter:4016=start",
    "https://logboek.belastingdienst.nl/data-processing-operations=Belastingdienst=http://logboek-bd:4016",
    "https://logboek.rvig.nl/data-processing-operations=RvIG=http://logboek-brp:4016",
    "https://logboek.gbo.overheid.nl/toestemming/data-processing-operations=GBO toestemming=http://logboek-toestemming:4016",
    "https://logboek.installatieregister.test/data-processing-operations=Installatie Register=http://logboek-ir:4016=start",
    "https://logboek.lvg.test/data-processing-operations=LVG=http://logboek-lvg:4016");

var devPortalBackend = builder.AddDockerfile("dev-portal-backend", "..", "services/dev-portal-backend/Dockerfile")
    .WithHttpEndpoint(targetPort: 4007, name: "http")
    .WithHttpHealthCheck("/health")
    .WithEnvironment("OTEL_SERVICE_NAME", "dev-portal-backend")
    .WithEnvironment("CITIZENS_FILE", "/citizens/citizens.json")
    .WithEnvironment("ORGANIZATIONS_FILE", "/organizations.json")
    .WithEnvironment("LDV_LOGBOOKS", ldvLogbooks)
    .WithEnvironment("LDV_READ_TOKEN", ldvReadToken)
    .WithEnvironment("QTSP_URL", "http://qtsp-reference:4023")
    .WithEnvironment("TEST_PID_OFFERS_URL", "http://eudi-credential-issuer:8081/offers")
    .WithEnvironment("TEST_PID_PERSONS_FILE", "/test-pid-persons/personen.json")
    .WithEnvironment("CREDENTIAL_ISSUER_STATUS_URL", "http://eudi-credential-issuer:8080")
    .WithEnvironment("EUDI_OFFERS_URL", "http://eudi-adapter:4009/eudi-offers.json")
    .WithEnvironment("FSC_GROUP_ID", "fsc-demo")
    .WithEnvironment("POLICIES_DIR", "/policies")
    .WithEnvironment("RULES_FILE", "/rules.json")
    .WithEnvironment("ADL_DATABASE_URL", ReferenceExpression.Create($"postgres://adl_reader:{ftvAdlReaderPassword}@postgres-ftv:5432/ftv_adl?sslmode=disable"))
    .WithVolume("gbo-dev-portal-var", "/var")
    .WithBindMount(Repo("services/graphql-server/mockdata"), "/citizens", isReadOnly: true)
    .WithBindMount(Repo("services/brp-graphql-server/mockdata"), "/test-pid-persons", isReadOnly: true)
    .WithBindMount(Repo("services/dev-portal-backend/organizations.json"), "/organizations.json", isReadOnly: true)
    .WithBindMount(Repo("policies"), "/policies", isReadOnly: true)
    .WithBindMount(Repo("services/dev-portal-backend/rules.json"), "/rules.json", isReadOnly: true)
    .WaitFor(openftvPdp)
    .WaitForCompletion(postgresFtvInit);

foreach (var (org, dir) in new[] { ("edi", "edi-issuer"), ("hv", "hypotheekverlener-mock"), ("ir", "installatieregister"), ("asip", "asip"), ("bd", "belastingdienst-mock") })
{
    var upper = org.ToUpperInvariant();
    devPortalBackend
        .WithEnvironment($"FSC_TXLOG_{upper}_URL", $"https://{org}-txlog-api:9443")
        .WithEnvironment($"FSC_TXLOG_{upper}_CERT", $"/certs/{org}/internal-cert.pem")
        .WithEnvironment($"FSC_TXLOG_{upper}_KEY", $"/certs/{org}/internal-cert-key.pem")
        .WithEnvironment($"FSC_TXLOG_{upper}_CA", $"/certs/{org}/intermediate_ca.pem")
        .WithBindMount(Repo($"fsc-infra/orgs/{dir}/pki/internal"), $"/certs/{org}", isReadOnly: true);
}

// The Vite dev server runs on the host; its proxy targets are the services'
// host endpoints.
builder.AddViteApp("developer-portal", "../developer-portal")
    // npm ci: install exactly the lockfile, never rewrite it.
    .WithNpm(install: true, installCommand: "ci")
    .WithEnvironment("DEV_API_TARGET", devPortalBackend.GetEndpoint("http"))
    .WithEnvironment("JAEGER_API_TARGET", jaeger.GetEndpoint("http"))
    .WithEnvironment("BD_GRAPHQL_TARGET", graphqlServer.GetEndpoint("http"))
    .WithEnvironment("BRP_GRAPHQL_TARGET", brpGraphqlServer.GetEndpoint("http"))
    .WithEnvironment("VITE_JAEGER_PUBLIC_URL", jaeger.GetEndpoint("http"))
    .WithEnvironment("VITE_EUDI_UL_BASE", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance")
    .WaitFor(devPortalBackend);

// ── Grouping in Docker Desktop and OrbStack ──────────────────────────────
// Both group containers by the compose project label. Every container the
// AppHost starts carries it, with its resource name as the service. The
// project name is its own, so `docker compose -p gbo-demo down` cannot touch
// these containers.
const string ComposeProject = "gbo-demo-aspire";
foreach (var container in builder.Resources.OfType<ContainerResource>().ToList())
{
    builder.CreateResourceBuilder(container).WithContainerRuntimeArgs(
        "--label", $"com.docker.compose.project={ComposeProject}",
        "--label", $"com.docker.compose.service={container.Name}");
}

builder.Build().Run();
