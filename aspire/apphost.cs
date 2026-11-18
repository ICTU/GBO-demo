#:package Aspire.Hosting.AppHost@17.0.0
#:package Aspire.Hosting.Dotnet@17.0.0
#:package Aspire.Hosting.JavaScript@17.0.0
#:package Aspire.Hosting.Docker@17.0.0

// GBO-demo AppHost. Run it from this directory with `aspire run`; see
// README.md for the profiles and settings, and PLAN.md for the migration.
//
// The Go services run as containers built from their own Dockerfiles, with
// the same hostnames, container ports and mounts they had under compose. The
// FSC seeds, the PIP pull config, the FSC Inways and the collector address
// them by name, and Aspire's container network resolves resource names the
// same way compose did.

using System.Security.Cryptography;
using Aspire.Hosting.JavaScript;

var builder = DistributedApplication.CreateBuilder(args);

// `aspire publish` writes a docker-compose.yaml for running the same model
// without Aspire. It is generated output (aspire-output/), never edited.
builder.AddDockerComposeEnvironment("compose");

var repoRoot = Path.GetFullPath(Path.Combine(builder.AppHostDirectory, ".."));
string Repo(string relative) => Path.Combine(repoRoot, relative);

// ── Profiles ─────────────────────────────────────────────────────────────
// Which parts of the demo run, as the compose profiles did. Set them with a
// launch profile (aspire run --launch-profile dvtp) or Gbo__Profiles, comma
// separated. Without any, only the core runs.
//   dvtp        consent flow over FSC: consumer, consent portal, IR, LVG
//   eudi        EUDI wallet issuance over FSC (needs public URLs and CAs)
//   full        dvtp + eudi
//   onboarding  source registry and reconciler only
//   integrator  the integrator peer and backend (with dvtp or full)
//   asi         the ASI route: ASIP, authorization server, QTSP (with eudi or full)
//   manager     the OpenFTV Manager owning policy distribution
//   credential-issuer, wallet-registration   see README.md
var profiles = new HashSet<string>(
    (builder.Configuration["Gbo:Profiles"] ?? "")
        .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries),
    StringComparer.OrdinalIgnoreCase);
if (profiles.Contains("full")) { profiles.Add("dvtp"); profiles.Add("eudi"); }
if (profiles.Contains("eudi") || profiles.Contains("credential-issuer") || profiles.Contains("wallet-registration") || profiles.Contains("asi")) { profiles.Add("onboarding"); }
bool On(string profile) => profiles.Contains(profile);
var fscOn = On("dvtp") || On("eudi") || On("onboarding") || On("integrator") || On("asi");

// ── Settings ─────────────────────────────────────────────────────────────
// Browser-facing links, as GBO_PUBLIC_HOST and GBO_PUBLIC_SCHEME were.
var publicHost = builder.Configuration["Gbo:PublicHost"] ?? "localhost";
var publicScheme = builder.Configuration["Gbo:PublicScheme"] ?? "http";
string PublicUrl(int port) => $"{publicScheme}://{publicHost}:{port}";

// ── Secrets ──────────────────────────────────────────────────────────────
// Database passwords are generated on first run and kept in the AppHost
// user secrets (aspire secret list). The LDV values default to the local
// demo values compose used; set real ones with aspire secret set.
IResourceBuilder<ParameterResource> Generated(string name) =>
    builder.AddParameter(name, new GenerateParameterDefault { MinLength = 24, Special = false }, secret: true, persist: true);
var ftvPostgresPassword = Generated("ftv-postgres-password");
var ftvAdlReaderPassword = Generated("ftv-adl-reader-password");
var fscPostgresPassword = Generated("fsc-postgres-password");
var ldvWriteToken = builder.AddParameter("ldv-write-token", "local-demo-logboek-token", secret: true);
var ldvReadToken = builder.AddParameter("ldv-read-token", "local-demo-logboek-read-token", secret: true);
var pseudonymKeyBd = builder.AddParameter("ldv-subject-pseudonym-key", "local-demo-pseudonym-key", secret: true);
var pseudonymKeyBrp = builder.AddParameter("ldv-subject-pseudonym-key-brp", "local-demo-pseudonym-key-brp", secret: true);
var pseudonymKeyGbo = builder.AddParameter("ldv-subject-pseudonym-key-gbo", "local-demo-pseudonym-key-gbo", secret: true);
var pseudonymKeyLvg = builder.AddParameter("ldv-subject-pseudonym-key-lvg", "local-demo-pseudonym-key-lvg", secret: true);
var subjectRefKey = builder.AddParameter("subject-ref-key", "local-demo-subject-ref-key-not-a-secret", secret: true);
var bsnAuthorisedOins = builder.AddParameter("bsn-authorised-oins", "99999999900000000200");

// ── Telemetry ────────────────────────────────────────────────────────────
// The services export to the collector; the collector forwards to the
// dashboard with the dashboard's OTLP API key. The key is fixed here so the
// collector can be given it: AppHost:OtlpApiKey from configuration, or a
// fresh random one per run.
if (string.IsNullOrEmpty(builder.Configuration["AppHost:OtlpApiKey"]))
{
    builder.Configuration["AppHost:OtlpApiKey"] = Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
}
// Under `aspire run` the dashboard listens on the host; in a published
// compose file it is the compose environment's dashboard container.
var dashboardOtlpGrpc = "compose-dashboard:18889";
if (builder.ExecutionContext.IsRunMode)
{
    var dashboardOtlp = new Uri(builder.Configuration["ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL"]
        ?? throw new InvalidOperationException("ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL is not set; start the AppHost with `aspire run`."));
    if (dashboardOtlp.Scheme != "http")
    {
        throw new InvalidOperationException("The collector forwards to the dashboard over plaintext gRPC; use an http launch profile.");
    }
    dashboardOtlpGrpc = "host.docker.internal:" + dashboardOtlp.Port;
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
    .WithEnvironment("ASPIRE_DASHBOARD_OTLP_GRPC", dashboardOtlpGrpc)
    .WithEnvironment("ASPIRE_DASHBOARD_OTLP_API_KEY", builder.Configuration["AppHost:OtlpApiKey"]!)
    .WaitFor(jaeger);

// A service built from its Dockerfile, exporting spans to the collector.
IResourceBuilder<ContainerResource> GoService(string name, string dockerfile, int port, string? context = null, int? hostPort = null)
{
    return builder.AddDockerfile(name, context ?? "..", dockerfile)
        .WithHttpEndpoint(port: hostPort, targetPort: port, name: "http")
        .WithHttpHealthCheck("/health")
        .WithEnvironment("OTEL_SERVICE_NAME", name)
        .WithEnvironment("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4317")
        .WaitFor(otelCollector);
}

// ── PKI tooling ──────────────────────────────────────────────────────────
// The pki-tools image (cfssl, openssl, curl, jq, psql) signs the FSC
// certificates and runs the seeds. Built once, cached by Docker.
var pkiToolsImage = builder.AddExecutable("pki-tools-image", "docker", Repo("fsc-infra/pki"),
    "build", "-q", "-f", "Dockerfile.pki", "-t", "gbo-demo/pki-tools:local", ".");

// The PDP's TLS certificate (SAN openftv-pdp), made once. Skipped when it exists.
var pdpCert = builder.AddExecutable("pdp-cert", "bash", repoRoot, "fsc-infra/pki/generate-pdp-cert.sh")
    .WaitForCompletion(pkiToolsImage);

// A broken Rego module is swallowed by the PDP; this stops the start instead.
var policyCheck = builder.AddExecutable("policy-check", "bash", repoRoot, "scripts/policy-check.sh");

// A run-once step in the pki-tools image, on the AppHost's network, with
// fsc-infra mounted at /work. Retried, since the FSC Managers have no health
// check to wait for; the seed scripts are idempotent.
IResourceBuilder<ContainerResource> PkiStep(string name, string script, params (string Key, string Value)[] env)
{
    var step = builder.AddContainer(name, "gbo-demo/pki-tools", "local")
        .WithImagePullPolicy(ImagePullPolicy.Never)
        .WithBindMount(Repo("fsc-infra"), "/work", isReadOnly: true)
        .WithEnvironment("FSC_POSTGRES_PASSWORD", fscPostgresPassword)
        .WithEntrypoint("bash")
        .WithArgs("-c", $"cd /work && for i in $(seq 1 10); do bash {script} && exit 0; echo \"attempt $i failed, retrying\" >&2; sleep 6; done; exit 1")
        .WaitForCompletion(pkiToolsImage);
    foreach (var (key, value) in env)
    {
        step.WithEnvironment(key, value);
    }
    return step;
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
    .WithEnvironment("CONSENT_STORE", "memory")
    .WithEnvironment("CONSENT_TOKEN_ISSUER", "https://consent-register.gbo.test")
    .WithEnvironment("CONSENT_TOKEN_AUDIENCE", "gbo:dvtp:pdp")
    .WithEnvironment("STATUS_PORT", "4012")
    .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-toestemming:4016")
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyGbo)
    .WaitFor(logboeken["logboek-toestemming"]);

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
    .WithEnvironment("PDP_BUNDLE_MANAGER", On("manager") ? "http://openftv-manager:9443/v1/bundle/gbo-pdp" : "")
    .WithEnvironment("PDP_TLS_CERT", "/certs/pdp-service.pem")
    .WithEnvironment("PDP_TLS_KEY", "/certs/pdp-service-key.pem")
    .WithBindMount(Repo("policies"), "/policies", isReadOnly: true)
    .WithBindMount(Repo("schemas/pdp-mirror"), "/etc/gbo/ftv-graphql", isReadOnly: true)
    .WithBindMount(Repo("services/openftv-pdp/certs"), "/certs", isReadOnly: true)
    .WithBindMount(Repo("services/openftv-pdp/pip-pull"), "/pip-pull", isReadOnly: true)
    .WaitForCompletion(policyCheck)
    .WaitForCompletion(postgresFtvInit)
    .WaitForCompletion(pdpCert)
    .WaitFor(consentRegister)
    .WaitFor(dvtpOnboardingRegister);

// The source Managers decide on incoming contracts through the PDP, so the
// PDP must answer with the seeded Hypotheek-BV admission before the first
// contract is submitted (the PIP pull takes a few seconds after start).
var pdpAdmission = builder.AddContainer("pdp-admission", "gbo-demo/pki-tools", "local")
    .WithImagePullPolicy(ImagePullPolicy.Never)
    .WithBindMount(Repo("services/openftv-pdp/certs/pdp-service.pem"), "/pdp-ca.pem", isReadOnly: true)
    .WithEntrypoint("bash")
    .WithArgs("-c", """
        request='{"subject":{"type":"peer_id","id":"99999999900000000200","properties":{"self_peer_id":"99999999900000000200","peer_ids":["99999999900000000200","99999999900000000300"]}},"action":{"name":"autosign_contract"},"resource":{"type":"contract","id":"pdp-admission-readiness","properties":{"grant_types":[2]}},"context":{"doelbinding":"auto_sign_contract"}}'
        for i in $(seq 1 60); do
          if curl -fsS --max-time 2 --cacert /pdp-ca.pem -H 'Content-Type: application/json' -d "$request" https://openftv-pdp:8443/authzen/v1/evaluation | jq -e '.decision == true' >/dev/null 2>&1; then
            echo "OpenFTV admission data ready"; exit 0
          fi
          sleep 1
        done
        echo "OpenFTV did not load the seeded Hypotheek-BV admission within 60s" >&2; exit 1
        """)
    .WaitForCompletion(pkiToolsImage)
    .WaitFor(openftvPdp);

// ── Sources: Belastingdienst, RvIG, LVG and an unsecured demo source ─────
var bsnkMock = GoService("bsnk-mock", "Dockerfile", 4003, context: "../services/bsnk-mock")
    .WithEnvironment("BSN_AUTHORISED_OINS", bsnAuthorisedOins);

var decryptionComponent = GoService("decryption-component", "Dockerfile", 4003, context: "../services/bsnk-mock")
    .WithEnvironment("DECRYPTION_COMPONENT_ONLY", "true");

var graphqlServer = GoService("graphql-server", "services/graphql-server/Dockerfile", 4000)
    .WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata.json")
    .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-bd:4016")
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyBd)
    .WithEnvironment("LDV_YEAR_ACTIVITY_TEMPLATE", "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-%d/v1")
    .WithBindMount(Repo("services/graphql-server/config"), "/config", isReadOnly: true)
    .WithVolume("gbo-ldv-outbox-graphql-server", "/data")
    .WaitFor(logboeken["logboek-bd"]);

var brpGraphqlServer = GoService("brp-graphql-server", "services/brp-graphql-server/Dockerfile", 4001)
    .WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata.json")
    .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-brp:4016")
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyBrp)
    .WithBindMount(Repo("services/brp-graphql-server/config"), "/config", isReadOnly: true)
    .WithVolume("gbo-ldv-outbox-brp-graphql-server", "/data")
    .WaitFor(logboeken["logboek-brp"]);

GoService("unsecured-graphql-server", "services/graphql-server/Dockerfile", 4000)
    .WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata-unsecured.json")
    .WithBindMount(Repo("services/graphql-server/config"), "/config", isReadOnly: true);

// The source-side sidecar in front of a source: decrypts the polymorphic
// identity and writes the source's logbook records.
IResourceBuilder<ContainerResource> Sidecar(string name, string upstream, string logbook,
    IResourceBuilder<ParameterResource> pseudonymKey, string activityBase, string activityPrefix,
    IResourceBuilder<ContainerResource> source)
{
    return GoService(name, "services/bron-sidecar/Dockerfile", 4011)
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

IResourceBuilder<ContainerResource>? lvgGraphqlServer = null;
if (On("dvtp"))
{
    lvgGraphqlServer = GoService("lvg-graphql-server", "services/lvg-graphql-server/Dockerfile", 4008)
        .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-lvg:4016")
        .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
        .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyLvg)
        .WithEnvironment("LDV_ACTIVITY_BASE", "https://logboek.lvg.test/verwerkingsactiviteiten")
        .WithVolume("gbo-ldv-outbox-lvg-graphql-server", "/data")
        .WaitFor(logboeken["logboek-lvg"]);
    Sidecar("lvg-sidecar", "http://lvg-graphql-server:4008", "logboek-lvg", pseudonymKeyLvg,
        "https://logboek.lvg.test/verwerkingsactiviteiten", "lvg", lvgGraphqlServer);
}

// ── FSC network (OpenFSC v2.4.0) ─────────────────────────────────────────
// One Postgres for all peers, the group CA (cfssl + certportal), and per
// peer a controller, manager, txlog-api and inway or outway, each with its
// migrations. Certificates are made by the unchanged fsc-infra/pki scripts,
// run on the host so the CA keys stay outside the service containers.
var fscPeers = new Dictionary<string, (IResourceBuilder<ContainerResource> Manager, IResourceBuilder<ContainerResource> Controller)>();
IResourceBuilder<ContainerResource>? fscReady = null;
var fscSeeds = new List<IResourceBuilder<ContainerResource>>();

if (fscOn)
{
    const string FscVersion = "v2.4.0";
    const string DirectoryPeerId = "99999999900000000000";

    var fscRootCa = builder.AddExecutable("fsc-root-ca", "bash", repoRoot, "fsc-infra/pki/generate-root-ca.sh")
        .WaitForCompletion(pkiToolsImage);

    var cfssl = builder.AddContainer("cfssl", "gbo-demo/pki-tools", "local")
        .WithImagePullPolicy(ImagePullPolicy.Never)
        .WithBindMount(Repo("fsc-infra/pki"), "/pki", isReadOnly: true)
        .WithArgs("cfssl", "serve", "-address=0.0.0.0", "-port=8888",
            "-ca=/pki/ca/intermediate.pem", "-ca-key=/pki/ca/intermediate-key.pem",
            "-config=/pki/cfssl-signing-config.json")
        .WaitForCompletion(fscRootCa);

    var certportal = builder.AddContainer("certportal", "federatedserviceconnectivity/ca-certportal", FscVersion)
        .WithHttpEndpoint(port: 8091, targetPort: 8080, name: "http")
        .WithArgs("/usr/local/bin/ca-certportal", "serve", "--listen-address=0.0.0.0:8080",
            "--ca-host=http://cfssl:8888", "--include-issuer-cert=true")
        .WaitFor(cfssl);

    var fscPostgres = builder.AddContainer("fsc-postgres", "postgres", "17.10-alpine")
        .WithContainerNetworkAlias("postgres")
        .WithEndpoint(targetPort: 5432, name: "tcp", scheme: "tcp")
        .WithEnvironment("POSTGRES_USER", "postgres")
        .WithEnvironment("POSTGRES_PASSWORD", fscPostgresPassword)
        .WithArgs("postgres", "-c", "max_connections=300")
        .WithVolume("gbo-fsc-postgres-data", "/var/lib/postgresql/data");

    // Creates every peer database that does not exist yet, also in a volume
    // initialised before a peer was added.
    var fscDatabases = builder.AddContainer("fsc-databases", "postgres", "17.10-alpine")
        .WithEnvironment("PGHOST", "postgres")
        .WithEnvironment("PGUSER", "postgres")
        .WithEnvironment("PGPASSWORD", fscPostgresPassword)
        .WithEntrypoint("sh")
        .WithArgs("-c", """
            for i in $(seq 1 60); do psql -d postgres -tAc 'select 1' >/dev/null 2>&1 && break; sleep 1; done
            for org in dir edi bd hv brp cr pdp ir int asip; do
              for part in controller manager txlog; do
                db="fsc_${org}_${part}"
                [ "$(psql -d postgres -tAc "select 1 from pg_database where datname='$db'")" = 1 ] || createdb "$db"
              done
            done
            echo "FSC databases ready"
            """)
        .WaitFor(fscPostgres);

    // One peer: certificates, migrations, controller, manager, txlog-api and
    // an inway (provider) or outway (consumer).
    void AddFscPeer(string prefix, string pkiDir, string certName, string bootstrap, string db,
        int controllerPort, string? inway = null, string? outway = null, bool authzen = false,
        string[]? managerArgs = null, int? outwayPort = null)
    {
        var certs = builder.AddExecutable($"{prefix}-certs", "bash", repoRoot,
                "aspire/scripts/fsc-pki.sh", $"fsc-infra/pki/{bootstrap}")
            .WaitFor(certportal);

        IResourceBuilder<ContainerResource> Fsc(string name, string component)
        {
            var c = builder.AddContainer(name, $"federatedserviceconnectivity/{component}", FscVersion)
                .WithBindMount(Repo($"fsc-infra/{pkiDir}"), "/certs", isReadOnly: true)
                .WithEnvironment("TLS_GROUP_ROOT_CERT", "/certs/org/root.pem")
                .WithEnvironment("TLS_ROOT_CERT", "/certs/internal/intermediate_ca.pem")
                .WithEnvironment("TLS_CERT", "/certs/internal/internal-cert.pem")
                .WithEnvironment("TLS_KEY", "/certs/internal/internal-cert-key.pem")
                .WithEnvironment("GROUP_ID", "fsc-demo")
                .WaitForCompletion(certs);
            return c;
        }
        ReferenceExpression Dsn(string part) =>
            ReferenceExpression.Create($"postgresql://postgres:{fscPostgresPassword}@postgres:5432/fsc_{db}_{part}?sslmode=disable&connect_timeout=2");
        IResourceBuilder<ContainerResource> Migrations(string component, string part) =>
            Fsc($"{prefix}-migrations-{component}", component)
                .WithArgs($"/usr/local/bin/{component}", "migrate", "up")
                .WithEnvironment("POSTGRES_DSN", Dsn(part))
                .WaitForCompletion(fscDatabases);

        var migrateController = Migrations("controller", "controller");
        var migrateManager = Migrations("manager", "manager");
        var migrateTxlog = Migrations("txlog-api", "txlog");

        var txlog = Fsc($"{prefix}-txlog-api", "txlog-api")
            .WithEnvironment("STORAGE_POSTGRES_DSN", Dsn("txlog"))
            .WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:9443")
            .WithEnvironment("LISTEN_ADDRESS_PLAIN", "0.0.0.0:8080")
            .WaitForCompletion(migrateTxlog);

        var manager = Fsc($"{prefix}-manager", "manager")
            .WithEnvironment("TLS_GROUP_CERT", $"/certs/org/{certName}.pem")
            .WithEnvironment("TLS_GROUP_KEY", $"/certs/org/{certName}-key.pem")
            .WithEnvironment("TLS_GROUP_TOKEN_CERT", $"/certs/org/{certName}.pem")
            .WithEnvironment("TLS_GROUP_TOKEN_KEY", $"/certs/org/{certName}-key.pem")
            .WithEnvironment("TLS_GROUP_CONTRACT_CERT", $"/certs/org/{certName}.pem")
            .WithEnvironment("TLS_GROUP_CONTRACT_KEY", $"/certs/org/{certName}-key.pem")
            .WithEnvironment("TLS_INTERNAL_UNAUTHENTICATED_ROOT_CERT", "/certs/internal/intermediate_ca.pem")
            .WithEnvironment("TLS_INTERNAL_UNAUTHENTICATED_CERT", "/certs/internal/internal-cert.pem")
            .WithEnvironment("TLS_INTERNAL_UNAUTHENTICATED_KEY", "/certs/internal/internal-cert-key.pem")
            .WithEnvironment("LISTEN_ADDRESS_INTERNAL", "0.0.0.0:9443")
            .WithEnvironment("LISTEN_ADDRESS_INTERNAL_UNAUTHENTICATED", "0.0.0.0:9444")
            .WithEnvironment("LISTEN_ADDRESS_EXTERNAL", "0.0.0.0:8443")
            .WithEnvironment("CONTROLLER_REGISTRATION_API_ADDRESS", $"https://{prefix}-controller:9443")
            .WithEnvironment("TX_LOG_API_ADDRESS", $"https://{prefix}-txlog-api:9443")
            .WithEnvironment("SELF_ADDRESS", $"https://{prefix}-manager:8443")
            .WithEnvironment("DIRECTORY_MANAGER_ADDRESS", "https://directory-manager:8443")
            .WithEnvironment("DIRECTORY_PEER_ID", DirectoryPeerId)
            .WithEnvironment("STORAGE_POSTGRES_DSN", Dsn("manager"))
            .WaitForCompletion(migrateManager)
            .WaitFor(txlog);
        var args = new List<string> { "/usr/local/bin/manager", "serve" };
        args.AddRange(managerArgs ?? []);
        if (authzen)
        {
            // The Manager decides on incoming contracts through the PDP.
            args.Add("--authzen-auto-sign-pdp-address=https://openftv-pdp:8443/authzen/v1");
            args.Add("--authzen-auto-sign-pdp-root-ca=/authzen-ca/pdp-service.pem");
            manager.WithBindMount(Repo("services/openftv-pdp/certs"), "/authzen-ca", isReadOnly: true)
                .WaitForCompletion(pdpAdmission);
        }
        manager.WithArgs(args.ToArray());

        var controller = Fsc($"{prefix}-controller", "controller")
            .WithHttpEndpoint(port: controllerPort, targetPort: 3001, name: "http")
            .WithEnvironment("LISTEN_ADDRESS_UI", "0.0.0.0:3001")
            .WithEnvironment("LISTEN_ADDRESS_REGISTRATION_API", "0.0.0.0:9443")
            .WithEnvironment("LISTEN_ADDRESS_ADMINISTRATION_API", "0.0.0.0:9444")
            .WithEnvironment("MANAGER_ADDRESS_INTERNAL", $"https://{prefix}-manager:9443")
            .WithEnvironment("DIRECTORY_ADDRESS", "https://directory-manager:8443")
            .WithEnvironment("STORAGE_POSTGRES_DSN", Dsn("controller"))
            .WithEnvironment("AUDITLOG_TYPE", "stdout")
            .WaitForCompletion(migrateController)
            .WaitFor(manager);

        if (inway is not null)
        {
            var gw = Fsc($"{prefix}-inway", "inway")
                .WithEnvironment("TLS_GROUP_CERT", $"/certs/org/{certName}.pem")
                .WithEnvironment("TLS_GROUP_KEY", $"/certs/org/{certName}-key.pem")
                .WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:443")
                .WithEnvironment("NAME", inway)
                .WithEnvironment("SELF_ADDRESS", $"https://{prefix}-inway:443")
                .WithEnvironment("CONTROLLER_REGISTRATION_API_ADDRESS", $"https://{prefix}-controller:9443")
                .WithEnvironment("TX_LOG_API_ADDRESS", $"https://{prefix}-txlog-api:9443")
                .WithEnvironment("MANAGER_INTERNAL_UNAUTHENTICATED_ADDRESS", $"https://{prefix}-manager:9444")
                .WaitFor(controller);
            if (authzen && prefix != "cr")
            {
                // The Inway asks the PDP per request, with the body.
                gw.WithBindMount(Repo("services/openftv-pdp/certs"), "/authzen-ca", isReadOnly: true)
                    .WithEnvironment("AUTHZEN_SERVICE_ADDRESS", "https://openftv-pdp:8443/authzen/v1")
                    .WithEnvironment("AUTHZEN_ROOT_CA", "/authzen-ca/pdp-service.pem")
                    .WithEnvironment("AUTHZEN_WITH_BODY", "true");
            }
        }
        if (outway is not null)
        {
            var gw = Fsc($"{prefix}-outway", "outway")
                .WithEnvironment("TLS_GROUP_CERT", $"/certs/org/{certName}.pem")
                .WithEnvironment("TLS_GROUP_KEY", $"/certs/org/{certName}-key.pem")
                .WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:8080")
                .WithEnvironment("NAME", outway)
                .WithEnvironment("CONTROLLER_REGISTRATION_API_ADDRESS", $"https://{prefix}-controller:9443")
                .WithEnvironment("TX_LOG_API_ADDRESS", $"https://{prefix}-txlog-api:9443")
                .WithEnvironment("MANAGER_INTERNAL_ADDRESS", $"https://{prefix}-manager:9443")
                .WithEnvironment("ENABLE_GRANT_HASH_SUGGESTION", "true")
                .WaitFor(controller);
            if (outwayPort is int port)
            {
                // The host provisioning steps reach the EDI Outway here.
                gw.WithHttpEndpoint(port: port, targetPort: 8080, name: "http");
            }
        }
        fscPeers[prefix] = (manager, controller);
    }

    AddFscPeer("directory", "directory-peer/pki", "directory-peer", "bootstrap-directory-peer.sh", "dir", 8092,
        inway: "DirectoryInway-01", managerArgs: ["--auto-sign-grants=servicePublication"]);
    AddFscPeer("edi", "orgs/edi-issuer/pki", "edi-issuer", "bootstrap-edi-issuer.sh", "edi", 8094,
        outway: "EdiOutway-01", outwayPort: 8087);
    AddFscPeer("bd", "orgs/belastingdienst-mock/pki", "bd-mock", "bootstrap-bd-mock.sh", "bd", 8095,
        inway: "BdInway-01", authzen: true);
    AddFscPeer("hv", "orgs/hypotheekverlener-mock/pki", "hypotheekverlener", "bootstrap-hypotheekverlener.sh", "hv", 8096,
        outway: "HvOutway-01");
    AddFscPeer("cr", "orgs/consent-register/pki", "consent-register", "bootstrap-consent-register.sh", "cr", 8098,
        inway: "CrInway-01", authzen: true);
    AddFscPeer("pdp", "orgs/gbo-pdp/pki", "gbo-pdp", "bootstrap-gbo-pdp.sh", "pdp", 8099,
        outway: "PdpOutway-01");
    AddFscPeer("ir", "orgs/installatieregister/pki", "installatieregister", "bootstrap-installatieregister.sh", "ir", 8100,
        outway: "IrOutway-01");
    if (On("full"))
    {
        // The separate BRP/RvIG provider peer (...0400). No demo flow uses it:
        // the demo publishes BRP through the BD participant (...0200).
        AddFscPeer("brp", "orgs/brp-mock/pki", "brp-mock", "bootstrap-brp-mock.sh", "brp", 8097,
            inway: "BrpInway-01", authzen: true);
    }
    if (On("integrator"))
    {
        AddFscPeer("int", "orgs/integrator/pki", "integrator", "bootstrap-integrator.sh", "int", 8101,
            outway: "IntOutway-01");
    }
    if (On("asi"))
    {
        AddFscPeer("asip", "orgs/asip/pki", "asip", "bootstrap-asip.sh", "asip", 8102,
            outway: "AsipOutway-01");
    }

    // Its certificates come from the directory peer's bootstrap.
    builder.AddContainer("directory-ui", "federatedserviceconnectivity/directory-ui", FscVersion)
        .WithHttpEndpoint(port: 8093, targetPort: 3001, name: "http")
        .WithBindMount(Repo("fsc-infra/directory-ui/pki"), "/certs", isReadOnly: true)
        .WithEnvironment("TLS_GROUP_ROOT_CERT", "/certs/org/root.pem")
        .WithEnvironment("GROUP_ID", "fsc-demo")
        .WithEnvironment("LISTEN_ADDRESS", "0.0.0.0:3001")
        .WithEnvironment("DIRECTORY_MANAGER_ADDRESS", "https://directory-manager:8443")
        .WithEnvironment("TLS_GROUP_CERT", "/certs/org/directory-ui.pem")
        .WithEnvironment("TLS_GROUP_KEY", "/certs/org/directory-ui-key.pem")
        .WaitFor(fscPeers["directory"].Manager);

    // ── Contract seeds ───────────────────────────────────────────────────
    // Services, publications, connection contracts and grant links over mTLS
    // against the Managers and Controllers, in the order the Makefile ran
    // them. Each one waits for the previous.
    IResourceBuilder<ContainerResource>? previous = null;
    IResourceBuilder<ContainerResource> Seed(string name, string script, params (string, string)[] env)
    {
        var seed = PkiStep(name, script, env);
        foreach (var peer in fscPeers.Values)
        {
            seed.WaitFor(peer.Manager).WaitFor(peer.Controller);
        }
        if (previous is not null)
        {
            seed.WaitForCompletion(previous);
        }
        previous = seed;
        fscSeeds.Add(seed);
        return seed;
    }

    // BD publishes bri (BD source); the EDI issuer connects to it.
    Seed("fsc-seed-bri", "scripts/seed-bri-contract.sh");
    if (On("dvtp"))
    {
        // Hypotheek-BV (the DvTP consumer) connects to bri.
        Seed("fsc-seed-bri-hv", "scripts/seed-bri-connection-hv.sh");
        // The PDP asks the consent register for a consent's status over FSC.
        Seed("fsc-seed-consent-status", "scripts/seed-bri-contract.sh",
            ("SERVICE_NAME", "consent-status"),
            ("SERVICE_ENDPOINT_URL", "http://consent-register:4012"),
            ("SERVICE_INWAY_ADDRESS", "https://cr-inway:443"),
            ("PROVIDER_PEER_ID", "99999999900000000700"),
            ("PROVIDER_CONTROLLER_URL", "https://cr-controller:9444"),
            ("PROVIDER_MANAGER_URL", "https://cr-manager:9443"),
            ("PROVIDER_INTERNAL_DIR", "/work/orgs/consent-register/pki/internal"),
            ("CONSUMER_PEER_ID", "99999999900000000800"),
            ("CONSUMER_MANAGER_URL", "https://pdp-manager:9443"),
            ("CONSUMER_INTERNAL_DIR", "/work/orgs/gbo-pdp/pki/internal"),
            ("CONSUMER_ORG_CERT", "/work/orgs/gbo-pdp/pki/org/gbo-pdp.pem"),
            ("CONSUMER_CONTROLLER_DB", "fsc_pdp_controller"),
            ("GRANT_LINK_PATH", "/consent-status"),
            ("OUTWAY_NAME", "PdpOutway-01"));
        // LVG on the shared provider peer; the Installatie Register consumes it.
        Seed("fsc-seed-lvg", "scripts/seed-bri-contract.sh",
            ("SERVICE_NAME", "lvg"),
            ("SERVICE_ENDPOINT_URL", "http://lvg-sidecar:4011"),
            ("CONSUMER_PEER_ID", "99999999900000001000"),
            ("CONSUMER_MANAGER_URL", "https://ir-manager:9443"),
            ("CONSUMER_INTERNAL_DIR", "/work/orgs/installatieregister/pki/internal"),
            ("CONSUMER_ORG_CERT", "/work/orgs/installatieregister/pki/org/installatieregister.pem"),
            ("CONSUMER_CONTROLLER_DB", "fsc_ir_controller"),
            ("GRANT_LINK_PATH", "/lvg"),
            ("OUTWAY_NAME", "IrOutway-01"));
    }
    if (On("onboarding"))
    {
        // The logical RvIG source and the two metadata services on the BD peer.
        Seed("fsc-seed-rvig-source", "scripts/seed-bri-contract.sh",
            ("SERVICE_NAME", "brp"), ("SERVICE_ENDPOINT_URL", "http://brp-sidecar:4011"), ("CREATE_GRANT_LINK", "false"));
        Seed("fsc-seed-metadata-bd", "scripts/seed-bri-contract.sh",
            ("SERVICE_NAME", "gbo-metadata-bd"), ("SERVICE_ENDPOINT_URL", "http://graphql-server:4000"), ("CREATE_GRANT_LINK", "false"));
        Seed("fsc-seed-metadata-rvig", "scripts/seed-bri-contract.sh",
            ("SERVICE_NAME", "gbo-metadata-rvig"), ("SERVICE_ENDPOINT_URL", "http://brp-graphql-server:4001"), ("CREATE_GRANT_LINK", "false"));
    }
    if (On("integrator"))
    {
        // The integrator acts for Hypotheek-BV over a delegated connection.
        Seed("fsc-seed-int", "scripts/seed-bri-delegation-int.sh");
    }
    if (On("asi"))
    {
        // The ASIP connects to the BRP source through a grant link at /brp.
        Seed("fsc-seed-asip", "scripts/seed-bri-contract.sh",
            ("SERVICE_NAME", "brp"),
            ("SERVICE_ENDPOINT_URL", "http://brp-sidecar:4011"),
            ("CONSUMER_PEER_ID", "99999999900000001200"),
            ("CONSUMER_MANAGER_URL", "https://asip-manager:9443"),
            ("CONSUMER_INTERNAL_DIR", "/work/orgs/asip/pki/internal"),
            ("CONSUMER_ORG_CERT", "/work/orgs/asip/pki/org/asip.pem"),
            ("CONSUMER_CONTROLLER_DB", "fsc_asip_controller"),
            ("GRANT_LINK_PATH", "/brp"),
            ("OUTWAY_NAME", "AsipOutway-01"));
    }
    fscReady = previous;
}

// Waits until the FSC contracts are in place, when FSC runs.
IResourceBuilder<T> AfterFsc<T>(IResourceBuilder<T> resource) where T : IResourceWithWaitSupport
{
    if (fscReady is not null)
    {
        resource.WaitForCompletion(fscReady);
    }
    return resource;
}

// ── Developer portal ─────────────────────────────────────────────────────
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

// The FSC transaction logs it reads, per peer, with that peer's internal
// certificates. The directories exist once the peer's bootstrap ran.
foreach (var (org, dir) in new[] { ("edi", "edi-issuer"), ("hv", "hypotheekverlener-mock"), ("ir", "installatieregister"), ("asip", "asip"), ("bd", "belastingdienst-mock") })
{
    Directory.CreateDirectory(Repo($"fsc-infra/orgs/{dir}/pki/internal"));
    var upper = org.ToUpperInvariant();
    devPortalBackend
        .WithEnvironment($"FSC_TXLOG_{upper}_URL", $"https://{org}-txlog-api:9443")
        .WithEnvironment($"FSC_TXLOG_{upper}_CERT", $"/certs/{org}/internal-cert.pem")
        .WithEnvironment($"FSC_TXLOG_{upper}_KEY", $"/certs/{org}/internal-cert-key.pem")
        .WithEnvironment($"FSC_TXLOG_{upper}_CA", $"/certs/{org}/intermediate_ca.pem")
        .WithBindMount(Repo($"fsc-infra/orgs/{dir}/pki/internal"), $"/certs/{org}", isReadOnly: true);
}

// ── Consumers and the consent portal (dvtp) ──────────────────────────────
IResourceBuilder<ContainerResource>? consentPortalBackend = null;
IResourceBuilder<ContainerResource>? dienstverlenerBackend = null;
IResourceBuilder<ContainerResource>? irBackend = null;
if (On("dvtp"))
{
    consentPortalBackend = GoService("consent-portal-backend", "services/consent-portal-backend/Dockerfile", 4005)
        .WithEnvironment("BSNK_URL", "http://bsnk-mock:4003")
        .WithEnvironment("CONSENT_URL", "http://consent-register:4002")
        .WithEnvironment("DEV_PORTAL_BACKEND_URL", "http://dev-portal-backend:4007")
        .WithEnvironment("CONSENT_SOURCES", "99999999900000000200@20260101")
        .WithEnvironment("BSN_AUTHORISATION_LIST_MAX_AGE", "1h")
        .WithEnvironment("SUBJECT_REF_KEY", subjectRefKey)
        .WithEnvironment("SUBJECT_REF_KEY_VERSION", "1")
        .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-toestemming:4016")
        .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
        .WithEnvironment("LDV_BSNK_NEXT_LOGBOOK_ID", "https://github.com/ICTU/GBO-demo/tree/main/services/bsnk-mock")
        .WithVolume("gbo-ldv-outbox-consent-portal-backend", "/data")
        .WaitFor(bsnkMock)
        .WaitFor(consentRegister)
        .WaitFor(logboeken["logboek-toestemming"]);

    // A consumer backend, calling its source through its own FSC Outway.
    IResourceBuilder<ContainerResource> Consumer(string name, int port, string outway, string outwayPath,
        string sector, string logbook, string nextLogbooks, string? queryKind = null)
    {
        var consumer = GoService(name, "services/dienstverlener-backend/Dockerfile", port)
            .WithEnvironment("PORT", port.ToString())
            .WithEnvironment("OUTWAY_URL", $"http://{outway}:8080")
            .WithEnvironment("OUTWAY_PATH", outwayPath)
            .WithEnvironment("ORG_SECTOR", sector)
            .WithEnvironment("DEV_PORTAL_BACKEND_URL", "http://dev-portal-backend:4007")
            .WithEnvironment("LDV_LOGBOOK_URL", $"http://{logbook}:4016")
            .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
            .WithEnvironment("LDV_NEXT_LOGBOOK_IDS", nextLogbooks)
            .WithVolume($"gbo-ldv-outbox-{name}", "/data")
            .WaitFor(logboeken[logbook]);
        if (queryKind is not null)
        {
            consumer.WithEnvironment("QUERY_KIND", queryKind);
        }
        return AfterFsc(consumer);
    }

    dienstverlenerBackend = Consumer("dienstverlener-backend", 4006, "hv-outway", "/bri/graphql", "hypotheekverlener",
        "logboek-afnemer", "bd=https://logboek.belastingdienst.nl/data-processing-operations");
    irBackend = Consumer("ir-backend", 4010, "ir-outway", "/lvg/graphql", "installatieregister",
        "logboek-ir", "lvg=https://logboek.lvg.test/data-processing-operations", queryKind: "lvg");
    if (On("integrator"))
    {
        Consumer("integrator-backend", 4023, "int-outway", "/bri/graphql", "hypotheekverlener",
            "logboek-afnemer", "bd=https://logboek.belastingdienst.nl/data-processing-operations");
    }
}

// ── EUDI: source registry, onboarding, issuance (onboarding, eudi) ──────
// Host-only material lives outside the repository's tracked files, as with
// the Makefile: the pre-provisioned development CAs and every leaf signed
// from them stay in .local/secrets and are signed on the host with go run,
// so the CA keys never enter a container.
var secretsDir = builder.Configuration["Gbo:SecretsDir"] ?? Repo(".local/secrets");
var publicCertificatesDir = builder.Configuration["Gbo:PublicCertificatesDir"] ?? Repo(".local/public-certificates");
var developmentCaDir = Path.Combine(secretsDir, "development-ca");
var trustPublishDir = builder.Configuration["Gbo:TrustPublishDir"] ?? Repo(".local/public-trust");
var eudiUlBase = builder.Configuration["Gbo:EudiUlBase"] ?? "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance";

void RequireDevelopmentCas()
{
    foreach (var file in new[] { "issuer-ca-key.pem", "issuer-ca-cert.pem", "reader-ca-key.pem", "reader-ca-cert.pem" })
    {
        if (!File.Exists(Path.Combine(developmentCaDir, file)))
        {
            throw new InvalidOperationException(
                $"The {string.Join(",", profiles)} profile needs the pre-provisioned EUDI CA file {Path.Combine(developmentCaDir, file)}. " +
                "Put the CAs the test wallet trusts there, or point Gbo:SecretsDir at their directory (see README).");
        }
    }
}

// A host step: `go run .` in a Go module, skipped when its output exists.
IResourceBuilder<ExecutableResource> HostGoStep(string name, string module, string? skipWhenExists, params object[] args)
{
    if (skipWhenExists is not null && File.Exists(skipWhenExists))
    {
        return builder.AddExecutable(name, "true", repoRoot);
    }
    return builder.AddExecutable(name, "go", Repo($"services/{module}"), ["run", ".", .. args]);
}

IResourceBuilder<ContainerResource>? postgresEudi = null;
IResourceBuilder<ContainerResource>? sourceRegistryMigrations = null;
IResourceBuilder<ContainerResource>? sourceReconciler = null;
IResourceBuilder<ContainerResource>? eudiAdapter = null;
IResourceBuilder<ParameterResource>? sourceRegistryReaderPassword = null;
IResourceBuilder<ParameterResource>? eudiPostgresPassword = null;
IResourceBuilder<ParameterResource>? eudiPublicUrl = null;
IResourceBuilder<ParameterResource>? eudiBriUrl = null;
ReferenceExpression? SourceRegistryReaderUrl = null;

if (On("onboarding"))
{
    RequireDevelopmentCas();
    eudiPostgresPassword = Generated("eudi-postgres-password");
    var sourceRegistryPassword = Generated("source-registry-password");
    sourceRegistryReaderPassword = Generated("source-registry-reader-password");
    // Public HTTPS URLs: where the wallet reaches the issuance server, and
    // where the issuance server reaches the EUDI adapter. The issuance
    // server's host is the OpenID4VP client_id and a DNS SAN of every reader
    // certificate.
    eudiPublicUrl = builder.AddParameter("eudi-public-url")
        .WithDescription("Public HTTPS URL where the wallet reaches the issuance server, e.g. https://eudi-is.example.org/");
    eudiBriUrl = builder.AddParameter("eudi-bri-url")
        .WithDescription("Public HTTPS URL where the issuance server reaches the EUDI adapter, e.g. https://eudi-bri.example.org/");

    postgresEudi = builder.AddContainer("postgres-eudi", "postgres", "16-alpine")
        .WithEndpoint(targetPort: 5432, name: "tcp", scheme: "tcp")
        .WithEnvironment("POSTGRES_USER", "wallet")
        .WithEnvironment("POSTGRES_DB", "issuance_server")
        .WithEnvironment("POSTGRES_PASSWORD", eudiPostgresPassword)
        .WithVolume("gbo-postgres-eudi-data", "/var/lib/postgresql/data");

    var sourceRegistryBootstrap = builder.AddContainer("source-registry-bootstrap", "postgres", "16-alpine")
        .WithEnvironment("PGHOST", "postgres-eudi")
        .WithEnvironment("PGPORT", "5432")
        .WithEnvironment("PGUSER", "wallet")
        .WithEnvironment("PGPASSWORD", eudiPostgresPassword)
        .WithEnvironment("SOURCE_REGISTRY_PASSWORD", sourceRegistryPassword)
        .WithEnvironment("SOURCE_REGISTRY_READER_PASSWORD", sourceRegistryReaderPassword)
        .WithBindMount(Repo("scripts/bootstrap-source-registry.sh"), "/scripts/bootstrap-source-registry.sh", isReadOnly: true)
        .WithEntrypoint("sh")
        .WithArgs("-c", "for i in $(seq 1 60); do pg_isready -q && break; sleep 1; done; /scripts/bootstrap-source-registry.sh")
        .WaitFor(postgresEudi);

    SourceRegistryReaderUrl = ReferenceExpression.Create($"postgres://source_registry_reader:{sourceRegistryReaderPassword}@postgres-eudi:5432/source_registry");

    // The eudi-adapter image doubles as the CLI for migrations,
    // materialisation, reconciliation and exports.
    IResourceBuilder<ContainerResource> EudiAdapterCli(string name, params object[] args) =>
        builder.AddDockerfile(name, "..", "services/eudi-adapter/Dockerfile")
            .WithArgs(args)
            .WithEnvironment("SOURCE_REGISTRY_SCHEMA", "source_registry");

    sourceRegistryMigrations = EudiAdapterCli("source-registry-migrations", "./eudi-adapter", "migrate-source-registry")
        .WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", ReferenceExpression.Create($"postgres://source_registry:{sourceRegistryPassword}@postgres-eudi:5432/source_registry"))
        .WithEnvironment("SOURCE_REGISTRY_READER_ROLE", "source_registry_reader")
        .WaitForCompletion(sourceRegistryBootstrap);

    // Development leaves for the three demo sources, issued on the host by
    // the pre-provisioned development CAs (make onboard-demo-sources).
    var provisionSteps = new List<IResourceBuilder<ExecutableResource>>();
    foreach (var (id, oin, sourceName, logo) in new[]
    {
        ("belastingdienst", "99999999900000000200", "Belastingdienst", "assets/issuer-logos/belastingdienst.svg"),
        ("rvig", "99999999900000000200", "RvIG", "assets/issuer-logos/rvig.svg"),
        ("demo-unsecured", "99999999900000000900", "Demo unsecured source", "assets/issuer-logos/belastingdienst.svg"),
    })
    {
        provisionSteps.Add(HostGoStep($"provision-{id}-certificates", "eudi-adapter", null,
            "provision-development-certificates",
            $"--source-id={id}", $"--source-oin={oin}", $"--source-name={sourceName}",
            "--source-description=", $"--source-logo={Repo(logo)}",
            ReferenceExpression.Create($"--reader-public-url={eudiPublicUrl}"),
            $"--secrets-dir={secretsDir}", $"--public-certificates-dir={publicCertificatesDir}"));
    }

    // Publishes, transports through FSC, verifies and activates the source
    // metadata (make reconcile-sources).
    sourceReconciler = EudiAdapterCli("source-reconciler",
            "./eudi-adapter", "reconcile-sources", "--once",
            "--manager-url=https://edi-manager:9443",
            "--manager-ca=/fsc-certs/intermediate_ca.pem",
            "--manager-cert=/fsc-certs/internal-cert.pem",
            "--manager-key=/fsc-certs/internal-cert-key.pem",
            "--consumer-peer-id=99999999900000000100",
            "--outway-url=http://edi-outway:8080",
            "--schema=/config/schemas/gbo-source-metadata-v1.schema.json",
            ReferenceExpression.Create($"--type-metadata-base-url={eudiBriUrl}"),
            "--storage-backend=postgres",
            "--certificate-store=public-filesystem",
            "--database-schema=source_registry",
            "--secrets-dir=/public-certificates",
            ReferenceExpression.Create($"--reader-public-url={eudiPublicUrl}"),
            "--sources-dir=/config/sources",
            "--metadata-dir=/config/source-metadata",
            "--auto-promote")
        .WithEnvironment("EUDI_PUBLIC_URL", eudiPublicUrl)
        .WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", ReferenceExpression.Create($"postgres://source_registry:{sourceRegistryPassword}@postgres-eudi:5432/source_registry"))
        .WithBindMount(Repo("schemas"), "/config/schemas", isReadOnly: true)
        .WithBindMount(Repo("sources"), "/config/sources", isReadOnly: true)
        .WithBindMount(Repo("source-metadata"), "/config/source-metadata", isReadOnly: true)
        .WithBindMount(Repo("fsc-infra/orgs/edi-issuer/pki/internal"), "/fsc-certs", isReadOnly: true)
        .WithBindMount(publicCertificatesDir, "/public-certificates", isReadOnly: true)
        .WaitForCompletion(sourceRegistryMigrations)
        .WaitFor(graphqlServer)
        .WaitFor(brpGraphqlServer);
    AfterFsc(sourceReconciler);
    foreach (var step in provisionSteps)
    {
        sourceReconciler.WaitForCompletion(step);
    }

    if (On("eudi"))
    {
        // Reader, issuer, status-list and internal callback TLS certificates
        // of the reference QTSP.
        var qtspIssuanceCertificates = HostGoStep("provision-qtsp-issuance-certificates", "eudi-adapter", null,
            "provision-qtsp-issuance-certificates",
            "--qtsp-oin=99999999900000001300", "--qtsp-name=GBO demo test QTSP",
            ReferenceExpression.Create($"--reader-public-url={eudiPublicUrl}"),
            $"--secrets-dir={secretsDir}", "--tls-host=qtsp-reference");

        // No published image: built from the pinned nl-wallet submodule.
        var issuanceServerImage = builder.AddExecutable("eudi-issuance-server-image", "bash", repoRoot,
            "aspire/scripts/nlwallet-image.sh", "gbo/eudi-issuance-server:dev", "services/eudi-issuance-server/Dockerfile");

        var eudiMigrations = builder.AddContainer("eudi-migrations", "gbo/eudi-issuance-server", "dev")
            .WithImagePullPolicy(ImagePullPolicy.Never)
            .WithEntrypoint("/issuance_server_migrations")
            .WithArgs("up")
            .WithEnvironment("DATABASE_URL", ReferenceExpression.Create($"postgres://wallet:{eudiPostgresPassword}@postgres-eudi:5432/issuance_server"))
            .WaitForCompletion(issuanceServerImage)
            .WaitForCompletion(sourceRegistryBootstrap);

        // Renders the issuance server's configuration from the active Source
        // Release, into a host directory the server then mounts.
        var issuanceRuntime = Repo(".aspire-data/eudi-issuance-runtime");
        Directory.CreateDirectory(issuanceRuntime);
        var eudiIssuanceMaterialize = EudiAdapterCli("eudi-issuance-materialize",
                "./eudi-adapter", "materialize-issuance-release",
                "--database-schema=source_registry",
                "--secrets-dir=/source-certificates",
                "--template=/app/issuance_server.toml.example",
                ReferenceExpression.Create($"--adapter-base-url={eudiBriUrl}"),
                "--qtsp-attestation-url=https://qtsp-reference:4443/attestations",
                "--qtsp-type-metadata=/app/qtsp_attestation_metadata.json",
                "--output=/runtime/issuance_server.toml")
            .WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", SourceRegistryReaderUrl)
            .WithEnvironment("EUDI_PUBLIC_URL", eudiPublicUrl)
            .WithBindMount(secretsDir, "/source-certificates", isReadOnly: true)
            .WithBindMount(issuanceRuntime, "/runtime")
            .WaitForCompletion(sourceReconciler)
            .WaitForCompletion(qtspIssuanceCertificates);

        builder.AddContainer("eudi-issuance-server", "gbo/eudi-issuance-server", "dev")
            .WithImagePullPolicy(ImagePullPolicy.Never)
            .WithContainerRuntimeArgs("--user", "0")
            .WithHttpEndpoint(port: 18001, targetPort: 18001, name: "http")
            .WithEnvironment("ISSUANCE_SERVER__STORAGE__URL", ReferenceExpression.Create($"postgres://wallet:{eudiPostgresPassword}@postgres-eudi:5432/issuance_server"))
            .WithEnvironment("ISSUANCE_SERVER__UNIVERSAL_LINK_BASE_URL", "walletdebuginteraction://wallet.edi.rijksoverheid.nl/")
            .WithEnvironment("ISSUANCE_SERVER__WALLET_SERVER__IP", "0.0.0.0")
            .WithEnvironment("ISSUANCE_SERVER__WALLET_SERVER__PORT", "18001")
            .WithEnvironment("ISSUANCE_SERVER__INTERNAL_SERVER__IP", "0.0.0.0")
            .WithEnvironment("ISSUANCE_SERVER__INTERNAL_SERVER__PORT", "18002")
            .WithEnvironment("ISSUANCE_SERVER__PUBLIC_URL", eudiPublicUrl)
            .WithBindMount(Path.Combine(issuanceRuntime, "issuance_server.toml"), "/issuance_server.toml", isReadOnly: true)
            .WithBindMount(issuanceRuntime, "/config", isReadOnly: true)
            .WithVolume("gbo-eudi-tsl-publish", "/tsl-publish")
            .WaitForCompletion(eudiMigrations)
            .WaitForCompletion(eudiIssuanceMaterialize);

        eudiAdapter = GoService("eudi-adapter", "services/eudi-adapter/Dockerfile", 4009, hostPort: 9409)
            .WithEnvironment("FSC_OUTWAY_URL", "http://edi-outway:8080")
            .WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", SourceRegistryReaderUrl)
            .WithEnvironment("SOURCE_REGISTRY_SCHEMA", "source_registry")
            .WithEnvironment("SOURCE_REGISTRY_REFRESH_INTERVAL", "2s")
            .WithEnvironment("LDV_LOGBOOK_URL", "http://logboek-eudi-adapter:4016")
            .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
            .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", pseudonymKeyGbo)
            .WithEnvironment("LDV_NEXT_LOGBOOK_IDS", "belastingdienst=https://logboek.belastingdienst.nl/data-processing-operations,rvig=https://logboek.rvig.nl/data-processing-operations")
            .WithVolume("gbo-ldv-outbox-eudi-adapter", "/data")
            .WaitForCompletion(sourceReconciler)
            .WaitFor(logboeken["logboek-eudi-adapter"]);
    }

    if (On("wallet-registration"))
    {
        // Exports one source from the Source Registry for host-side signing;
        // run by hand (see README): it is started explicitly.
        EudiAdapterCli("wallet-registration-source", "./eudi-adapter", "export-wallet-registration-source")
            .WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", SourceRegistryReaderUrl)
            .WaitForCompletion(sourceRegistryMigrations)
            .WithExplicitStart();
    }

    if (On("credential-issuer"))
    {
        // Own credential issuer beside the issuance server (make credential-issuer-up).
        var issuerPublicUrl = builder.AddParameter("issuer-public-url")
            .WithDescription("Public URL of the own credential issuer, e.g. https://eudi-issuer.example.org");
        var trustPublicUrl = builder.AddParameter("eudi-trust-public-url")
            .WithDescription("Public URL of the trust host, e.g. https://eudi-trust.example.org");
        var credentialIssuerDbPassword = Generated("credential-issuer-db-password");
        if (!File.Exists(Path.Combine(trustPublishDir, "reader-root.crl")))
        {
            throw new InvalidOperationException("The credential-issuer profile needs the wallet registration first: run the provision-wallet-registration step (README).");
        }

        var credentialIssuerDbBootstrap = builder.AddContainer("credential-issuer-db-bootstrap", "postgres", "16-alpine")
            .WithEnvironment("PGHOST", "postgres-eudi")
            .WithEnvironment("PGPORT", "5432")
            .WithEnvironment("PGUSER", "wallet")
            .WithEnvironment("PGPASSWORD", eudiPostgresPassword)
            .WithEnvironment("CREDENTIAL_ISSUER_DB_PASSWORD", credentialIssuerDbPassword)
            .WithBindMount(Repo("scripts/bootstrap-credential-issuer.sh"), "/scripts/bootstrap-credential-issuer.sh", isReadOnly: true)
            .WithEntrypoint("sh")
            .WithArgs("-c", "for i in $(seq 1 60); do pg_isready -q && break; sleep 1; done; /scripts/bootstrap-credential-issuer.sh")
            .WaitFor(postgresEudi);
        var issuerDbUrl = ReferenceExpression.Create($"postgres://credential_issuer:{credentialIssuerDbPassword}@postgres-eudi:5432/credential_issuer");
        var credentialIssuerMigrations = builder.AddDockerfile("credential-issuer-migrations", "..", "services/eudi-credential-issuer/Dockerfile")
            .WithArgs("./eudi-credential-issuer", "migrate")
            .WithEnvironment("ISSUER_DATABASE_URL", issuerDbUrl)
            .WaitForCompletion(credentialIssuerDbBootstrap);
        var credentialIssuerMaterialize = EudiAdapterCli("credential-issuer-materialize",
                "./eudi-adapter", "materialize-credential-issuer",
                "--database-schema=source_registry", "--secrets-dir=/source-certificates",
                "--wallet-roots-dir=/wallet-roots", "--output-dir=/runtime-issuer", "--owner=1000")
            .WithEnvironment("SOURCE_REGISTRY_DATABASE_URL", SourceRegistryReaderUrl)
            .WithEnvironment("EUDI_BRI_URL", eudiBriUrl)
            .WithEnvironment("ISSUER_EUDI_KEY_STORAGE", builder.Configuration["Gbo:IssuerEudiKeyStorage"] ?? "")
            .WithEnvironment("ISSUER_EUDI_USER_AUTHENTICATION", builder.Configuration["Gbo:IssuerEudiUserAuthentication"] ?? "")
            .WithEnvironment("ISSUER_EUDI_PID_TRUSTED_AUTHORITIES", builder.Configuration["Gbo:IssuerEudiPidTrustedAuthorities"] ?? "")
            .WithEnvironment("TEST_PID_SOURCE_URL", "http://test-pid-source:8080/attestations")
            .WithEnvironment("EUDI_TRUST_PUBLIC_URL", trustPublicUrl)
            .WithEnvironment("GBO_PID_TRUST_LIST_URL", builder.Configuration["Gbo:PidTrustListUrl"] ?? "")
            .WithEnvironment("GBO_LOTE_SIGNER_ROOT_FILE", "/lote-root/lote-ca-cert.pem")
            .WithBindMount(secretsDir, "/source-certificates", isReadOnly: true)
            .WithBindMount(builder.Configuration["Gbo:WalletRootsDir"] ?? Repo(".local/wallet-roots"), "/wallet-roots", isReadOnly: true)
            .WithBindMount(Path.Combine(publicCertificatesDir, "development-ca"), "/lote-root", isReadOnly: true)
            .WithBindMount(builder.Configuration["Gbo:PidTrustAnchorsFile"] ?? Repo("services/asi-wallet-verifier/pid-issuer-anchors.txt"), "/pid-trust-anchors.txt", isReadOnly: true)
            .WithVolume("gbo-credential-issuer-runtime", "/runtime-issuer")
            .WaitForCompletion(sourceRegistryMigrations);

        GoService("test-pid-source", "Dockerfile", 8080, context: "../services/test-pid-source")
            .WithHttpHealthCheck("/healthz")
            .WithEnvironment("PERSONS_FILE", "/persons/personen.json")
            .WithBindMount(Repo("services/brp-graphql-server/mockdata/personen.json"), "/persons/personen.json", isReadOnly: true);

        builder.AddDockerfile("eudi-credential-issuer", "..", "services/eudi-credential-issuer/Dockerfile")
            .WithHttpEndpoint(port: 9429, targetPort: 8080, name: "http")
            .WithHttpEndpoint(port: 9431, targetPort: 8081, name: "internal")
            .WithHttpHealthCheck("/healthz")
            .WithEnvironment("ISSUER_PUBLIC_URL", issuerPublicUrl)
            .WithEnvironment("ISSUER_CONFIG_FILE", "/config/issuer.json")
            .WithEnvironment("ISSUER_DATABASE_URL", issuerDbUrl)
            .WithEnvironment("ISSUER_LISTEN_ADDR", ":8080")
            .WithEnvironment("ISSUER_INTERNAL_ADDR", ":8081")
            .WithEnvironment("ISSUER_LOGIN_SERVER", PublicUrl(9425))
            .WithEnvironment("ISSUER_LOGIN_URL_MAP", $"{PublicUrl(9425)}=http://asi-authorization-server:4021")
            .WithVolume("gbo-credential-issuer-runtime", "/config", isReadOnly: true)
            .WithBindMount(Repo("services/brp-graphql-server/mockdata"), "/test-pid-persons", isReadOnly: true)
            .WaitForCompletion(credentialIssuerMigrations)
            .WaitForCompletion(credentialIssuerMaterialize);

        builder.AddContainer("trust-host", "nginx", "1.27-alpine")
            .WithHttpEndpoint(port: 9430, targetPort: 8080, name: "http")
            .WithHttpHealthCheck("/healthz")
            .WithBindMount(Repo("trust-host/nginx.conf"), "/etc/nginx/conf.d/default.conf", isReadOnly: true)
            .WithBindMount(Repo("trust-host/pages"), "/usr/share/trust-pages", isReadOnly: true)
            .WithBindMount(trustPublishDir, "/usr/share/trust", isReadOnly: true);
    }
}

// ── ASI route: ASIP, authorization server, discovery, QTSP (asi) ────────
if (On("asi"))
{
    var asiAuthorizationServerUrl = builder.Configuration["Gbo:AsiAuthorizationServerPublicUrl"] ?? PublicUrl(9425);
    var asiProviderUrl = builder.Configuration["Gbo:AsiProviderPublicUrl"] ?? PublicUrl(9424);
    var qtspUrl = builder.Configuration["Gbo:QtspPublicUrl"] ?? PublicUrl(9427);
    var issuerUrl = builder.Configuration["Gbo:IssuerPublicUrl"] ?? "http://localhost:9429";

    // The development seal certificate of the ASIP mock and the test QTSP
    // certificate, issued on the host when missing (make demo-asi).
    var sealCertificate = HostGoStep("provision-asip-seal-certificate", "asi-provider",
        Path.Combine(secretsDir, "asi-provider/seal-cert.pem"),
        "provision-seal-certificate",
        $"--ca-key={Path.Combine(developmentCaDir, "issuer-ca-key.pem")}",
        $"--ca-cert={Path.Combine(developmentCaDir, "issuer-ca-cert.pem")}",
        $"--out-dir={Path.Combine(secretsDir, "asi-provider")}");
    var qtspCertificate = HostGoStep("provision-asip-qtsp-certificate", "asi-authorization-server",
        Path.Combine(secretsDir, "asi-qtsp/qtsp-cert.pem"),
        "provision-qtsp-certificate",
        $"--ca-key={Path.Combine(developmentCaDir, "reader-ca-key.pem")}",
        $"--ca-cert={Path.Combine(developmentCaDir, "reader-ca-cert.pem")}",
        $"--out-dir={Path.Combine(secretsDir, "asi-qtsp")}");
    foreach (var dir in new[] { "asi-authorization-server-wallet", "asi-authorization-server-eudi-wallet", "qtsp-reference" })
    {
        Directory.CreateDirectory(Path.Combine(secretsDir, dir));
    }

    var asiAuthorizationServer = builder.AddDockerfile("asi-authorization-server", "../services/asi-authorization-server")
        .WithHttpEndpoint(port: 9425, targetPort: 4021, name: "http")
        .WithHttpHealthCheck("/healthz")
        .WithEnvironment("AZSP_ISSUER", asiAuthorizationServerUrl)
        .WithEnvironment("AZSP_RESOURCES", $"{asiProviderUrl}/brp/authsrc-api=Basisregistratie Personen (mock)")
        .WithEnvironment("AZSP_ISSUANCE_RESOURCES", builder.Configuration["Gbo:AsiIssuanceResources"]
            ?? $"{issuerUrl}/belastingdienst/eudi=NTRNL-99990200=Belastingdienst,{issuerUrl}/rvig/eudi=NTRNL-99990300=RvIG,{issuerUrl}/demo-unsecured/eudi=NTRNL-99990900=Demo unsecured source")
        .WithEnvironment("AZSP_TRUST_ANCHORS_FILE", "/trust/reader-ca-cert.pem")
        .WithEnvironment("AZSP_ENTITLEMENT_RECHECK_INTERVAL", "1h")
        .WithEnvironment("AZSP_REQUIRE_AUTHORIZATION_DETAILS", builder.Configuration["Gbo:AsiRequireAuthorizationDetails"] ?? "false")
        .WithEnvironment("AZSP_WALLET_VERIFIER_URL", "http://asi-wallet-verifier:8002")
        .WithEnvironment("AZSP_WALLET_VERIFIER_STATUS_URL", "http://asi-wallet-verifier:8001")
        .WithEnvironment("AZSP_WALLET_VERIFIER_API_KEY_FILE", "/wallet-verifier/api-key")
        .WithEnvironment("AZSP_EUDI_WALLET_VERIFIER_URL", "http://eudi-wallet-verifier:8002")
        .WithEnvironment("AZSP_EUDI_WALLET_VERIFIER_STATUS_URL", "http://eudi-wallet-verifier:8001")
        .WithEnvironment("AZSP_EUDI_WALLET_VERIFIER_API_KEY_FILE", "/eudi-wallet-verifier/api-key")
        .WithBindMount(Path.Combine(developmentCaDir, "reader-ca-cert.pem"), "/trust/reader-ca-cert.pem", isReadOnly: true)
        .WithBindMount(Path.Combine(secretsDir, "asi-authorization-server-wallet"), "/wallet-verifier", isReadOnly: true)
        .WithBindMount(Path.Combine(secretsDir, "asi-authorization-server-eudi-wallet"), "/eudi-wallet-verifier", isReadOnly: true);

    var asiProvider = GoService("asi-provider", "services/asi-provider/Dockerfile", 4020, hostPort: 9424)
        .WithHttpHealthCheck("/healthz")
        .WithEnvironment("ASIP_PUBLIC_URL", asiProviderUrl)
        .WithEnvironment("ASIP_AUTHORIZATION_SERVER", asiAuthorizationServerUrl)
        .WithEnvironment("ASIP_AUTHORIZATION_SERVER_INTERNAL_URL", "http://asi-authorization-server:4021")
        .WithEnvironment("ASIP_OUTWAY_URL", "http://asip-outway:8080")
        .WithEnvironment("ASIP_SOURCE_REGISTRY_DATABASE_URL", SourceRegistryReaderUrl!)
        .WithEnvironment("ASIP_SEAL_KEY_FILE", "/seal/seal-key.pem")
        .WithEnvironment("ASIP_SEAL_CERT_FILE", "/seal/seal-cert.pem")
        .WithBindMount(Path.Combine(secretsDir, "asi-provider"), "/seal", isReadOnly: true)
        .WaitForCompletion(sealCertificate)
        .WaitForCompletion(sourceRegistryMigrations!)
        .WaitFor(asiAuthorizationServer);
    AfterFsc(asiProvider);

    var asiDiscovery = builder.AddDockerfile("asi-discovery-stub", "../services/asi-discovery-stub")
        .WithHttpEndpoint(port: 9426, targetPort: 4022, name: "http")
        .WithHttpHealthCheck("/healthz")
        .WithEnvironment("DIP_SOURCES", "http://asi-provider:4020/brp/dataservices")
        .WaitFor(asiProvider);

    builder.AddDockerfile("qtsp-reference", "../services/qtsp-reference")
        .WithHttpEndpoint(port: 9427, targetPort: 4023, name: "http")
        .WithHttpHealthCheck("/healthz")
        .WithEnvironment("QTSP_PUBLIC_URL", qtspUrl)
        .WithEnvironment("QTSP_DIP_URL", "http://asi-discovery-stub:4022")
        .WithEnvironment("QTSP_URL_MAP", $"{asiProviderUrl}=http://asi-provider:4020,{asiAuthorizationServerUrl}=http://asi-authorization-server:4021")
        .WithEnvironment("QTSP_KEY_FILE", "/qtsp/qtsp-key.pem")
        .WithEnvironment("QTSP_CERT_FILE", "/qtsp/qtsp-cert.pem")
        .WithEnvironment("QTSP_SEAL_TRUST_ANCHORS", "/anchors/issuer-ca-cert.pem")
        .WithEnvironment("QTSP_WALLET_VERIFIER_URL", "http://asi-wallet-verifier:8002")
        .WithEnvironment("QTSP_WALLET_VERIFIER_STATUS_URL", "http://asi-wallet-verifier:8001")
        .WithEnvironment("QTSP_WALLET_VERIFIER_API_KEY_FILE", "/wallet-verifier/api-key")
        .WithEnvironment("QTSP_EUDI_WALLET_VERIFIER_URL", "http://eudi-wallet-verifier:8002")
        .WithEnvironment("QTSP_EUDI_WALLET_VERIFIER_STATUS_URL", "http://eudi-wallet-verifier:8001")
        .WithEnvironment("QTSP_EUDI_WALLET_VERIFIER_API_KEY_FILE", "/eudi-wallet-verifier/api-key")
        .WithEnvironment("EUDI_PUBLIC_URL", eudiPublicUrl!)
        .WithEnvironment("EUDI_UL_BASE", eudiUlBase)
        .WithEnvironment("QTSP_ISSUANCE_ADDR", ":4443")
        .WithEnvironment("QTSP_ISSUANCE_TLS_KEY_FILE", "/issuance/tls-key.der.b64")
        .WithEnvironment("QTSP_ISSUANCE_TLS_CERT_FILE", "/issuance/tls-cert.der.b64")
        .WithEnvironment("QTSP_ISSUANCE_CALLBACK_PATH_FILE", "/issuance/callback-path")
        .WithEnvironment("QTSP_ISSUER_OFFERS_URL", builder.Configuration["Gbo:QtspIssuerOffersUrl"] ?? "")
        .WithEnvironment("QTSP_ISSUER_PROFILE", "wallet")
        .WithEnvironment("QTSP_ISSUER_CONFIGURATION", "qtsp-reference-attestation")
        .WithBindMount(Path.Combine(secretsDir, "asi-qtsp"), "/qtsp", isReadOnly: true)
        .WithBindMount(developmentCaDir, "/anchors", isReadOnly: true)
        .WithBindMount(Path.Combine(secretsDir, "asi-authorization-server-wallet"), "/wallet-verifier", isReadOnly: true)
        .WithBindMount(Path.Combine(secretsDir, "asi-authorization-server-eudi-wallet"), "/eudi-wallet-verifier", isReadOnly: true)
        .WithBindMount(Path.Combine(secretsDir, "qtsp-reference"), "/issuance", isReadOnly: true)
        .WaitForCompletion(qtspCertificate)
        .WaitFor(asiDiscovery);

    // The wallet verifiers run once they are provisioned (README, ASI
    // section); without them the authorization server offers DigiD (mock) only.
    var nlWalletVerifierConfig = Path.Combine(secretsDir, "asi-wallet-verifier/verification_server.toml");
    if (File.Exists(nlWalletVerifierConfig))
    {
        var verifierImage = builder.AddExecutable("asi-wallet-verifier-image", "bash", repoRoot,
            "aspire/scripts/nlwallet-image.sh", "gbo/asi-wallet-verifier:dev", "services/asi-wallet-verifier/Dockerfile");
        builder.AddContainer("asi-wallet-verifier", "gbo/asi-wallet-verifier", "dev")
            .WithImagePullPolicy(ImagePullPolicy.Never)
            .WithHttpEndpoint(port: 9428, targetPort: 8001, name: "http")
            .WithBindMount(nlWalletVerifierConfig, "/verification_server.toml", isReadOnly: true)
            .WaitForCompletion(verifierImage);
    }
    var eudiWalletVerifierDir = Path.Combine(secretsDir, "asi-eudi-wallet-verifier");
    if (File.Exists(Path.Combine(eudiWalletVerifierDir, "config.json")))
    {
        // Copies the provisioned files for the verifier's user (1000).
        var verifierRuntime = builder.AddDockerfile("eudi-wallet-verifier-runtime", "..", "services/eudi-adapter/Dockerfile")
            .WithArgs("./eudi-adapter", "project-runtime", "--from=/provisioned", "--to=/runtime", "--owner=1000")
            .WithContainerRuntimeArgs("--network", "none")
            .WithBindMount(eudiWalletVerifierDir, "/provisioned", isReadOnly: true)
            .WithVolume("gbo-eudi-wallet-verifier-runtime", "/runtime");
        builder.AddDockerfile("eudi-wallet-verifier", "..", "services/eudi-wallet-verifier/Dockerfile")
            .WithHttpEndpoint(port: 9432, targetPort: 8001, name: "http")
            .WithEnvironment("EUDI_WALLET_VERIFIER_CONFIG", "/config/config.json")
            .WithVolume("gbo-eudi-wallet-verifier-runtime", "/config", isReadOnly: true)
            .WaitForCompletion(verifierRuntime);
    }
}

// ── OpenFTV Manager owning policy distribution (manager) ─────────────────
if (On("manager"))
{
    var keycloakAdminPassword = Generated("keycloak-admin-password");
    var auditorPassword = Generated("ftv-manager-auditor-password");
    var deployPassword = Generated("ftv-manager-deploy-password");
    var oidcAuthority = builder.Configuration["Gbo:FtvOidcAuthority"] ?? $"{PublicUrl(9284)}/realms/gbo";
    var managerUrl = builder.Configuration["Gbo:FtvManagerUrl"] ?? PublicUrl(9280);

    var keycloak = builder.AddContainer("keycloak", "quay.io/keycloak/keycloak", "26.0.7")
        .WithHttpEndpoint(port: 9284, targetPort: 8080, name: "http")
        .WithHttpHealthCheck("/realms/gbo")
        .WithArgs("start-dev", "--import-realm", "--http-port=8080")
        .WithEnvironment("KC_BOOTSTRAP_ADMIN_USERNAME", "admin")
        .WithEnvironment("KC_BOOTSTRAP_ADMIN_PASSWORD", keycloakAdminPassword)
        .WithEnvironment("FTV_MANAGER_AUDITOR_PASSWORD", auditorPassword)
        .WithEnvironment("FTV_MANAGER_DEPLOY_PASSWORD", deployPassword)
        .WithEnvironment("KC_HOSTNAME_STRICT", "false")
        .WithEnvironment("KC_HTTP_ENABLED", "true")
        .WithEnvironment("KC_PROXY_HEADERS", "xforwarded")
        .WithBindMount(Repo("services/openftv-manager-ui/keycloak/realm-gbo.json"), "/opt/keycloak/data/import/realm-gbo.json", isReadOnly: true);

    var openftvManager = builder.AddContainer("openftv-manager", "registry.gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/manager", "v0.1.0")
        .WithHttpEndpoint(port: 9280, targetPort: 8443, name: "http")
        .WithHttpEndpoint(port: 9281, targetPort: 9443, name: "internal")
        .WithHttpEndpoint(port: 9282, targetPort: 8080, name: "health")
        .WithHttpHealthCheck("/healthz", endpointName: "health")
        .WithEnvironment("MANAGER_DATABASE_URL", ReferenceExpression.Create($"postgres://ftv:{ftvPostgresPassword}@postgres-ftv:5432/ftv_manager?sslmode=disable"))
        .WithEnvironment("MANAGER_POLICIES_STORE", "/authz")
        .WithEnvironment("MANAGER_POLICIES_STORE_RECURSE", "true")
        .WithEnvironment("MANAGER_AUTHORIZATION_AUTHENTICATE", "true")
        .WithEnvironment("MANAGER_AUTHORIZATION_FAIL_CLOSED_ON_EMPTY", "true")
        .WithEnvironment("MANAGER_TAGS_PATH", "/tags")
        .WithEnvironment("MANAGER_BUNDLE_CONFIGS", "/bundles")
        .WithEnvironment("MANAGER_INTERNAL_PORT", "9443")
        .WithEnvironment("MANAGER_LOG_LEVEL", "info")
        .WithEnvironment("MANAGER_OIDC_ISSUER", oidcAuthority)
        .WithEnvironment("MANAGER_OIDC_JWKS_URL", "http://keycloak:8080/realms/gbo/protocol/openid-connect/certs")
        .WithEnvironment("MANAGER_OIDC_AUDIENCE", "openftv-manager-api")
        .WithEnvironment("MANAGER_OIDC_ROLES_CLAIM", "roles")
        .WithEnvironment("MANAGER_ADL_PG_URL", ReferenceExpression.Create($"postgres://ftv:{ftvPostgresPassword}@postgres-ftv:5432/ftv_adl?sslmode=disable"))
        .WithBindMount(Repo("services/openftv-manager/authz"), "/authz", isReadOnly: true)
        .WithBindMount(Repo("services/openftv-manager/bundles"), "/bundles", isReadOnly: true)
        .WithBindMount(Repo("services/openftv-manager/tags"), "/tags", isReadOnly: true)
        .WaitForCompletion(postgresFtvInit)
        .WaitFor(keycloak);

    builder.AddContainer("openftv-manager-ui", "registry.gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/management-interface", "v0.1.0")
        .WithHttpEndpoint(port: 9283, targetPort: 8080, name: "http")
        .WithEnvironment("VITE_PAP_BASE_URL", managerUrl)
        .WithEnvironment("VITE_PIP_BASE_URL", managerUrl)
        .WithEnvironment("VITE_OIDC_AUTHORITY", oidcAuthority)
        .WithEnvironment("VITE_OIDC_CLIENT_ID", "openftv-manager-ui")
        .WithEnvironment("MANAGER_MAX_LOGO_SIZE", "102400")
        .WaitFor(openftvManager);

    // Pushes policies/ into the Manager; the PDP then starts and pulls the
    // bundle (make demo-manager / manager-seed). Rerun from the dashboard
    // after editing policies, then restart openftv-pdp.
    var managerSeed = builder.AddExecutable("manager-seed", "python3", repoRoot,
            "scripts/seed-openftv-manager.py", "--url", "http://localhost:9280")
        .WithEnvironment("FTV_MANAGER_DEPLOY_PASSWORD", deployPassword)
        .WaitForCompletion(policyCheck)
        .WaitFor(openftvManager);
    openftvPdp.WaitForCompletion(managerSeed);
}

// ── Public tunnel for the wallet (Gbo:CloudflareTunnel) ─────────────────
// The overlay docker-compose.cloudflare-tunnel.yml was: cloudflared with a
// tunnel token, routing the public EUDI, ASI and issuer hostnames here.
if (string.Equals(builder.Configuration["Gbo:CloudflareTunnel"], "true", StringComparison.OrdinalIgnoreCase))
{
    var tunnelToken = builder.AddParameter("cloudflare-tunnel-token", secret: true);
    var tunnel = builder.AddContainer("eudi-tunnel", "cloudflare/cloudflared", "latest")
        .WithArgs("tunnel", "--no-autoupdate", "run", "--token")
        .WithArgs(context => context.Args.Add(tunnelToken.Resource));
    foreach (var name in new[] { "eudi-issuance-server", "eudi-adapter", "asi-wallet-verifier", "eudi-wallet-verifier", "asi-authorization-server", "eudi-credential-issuer", "trust-host" })
    {
        if (builder.Resources.FirstOrDefault(r => r.Name == name) is { } resource)
        {
            tunnel.WaitFor(builder.CreateResourceBuilder(resource));
        }
    }
}

// ── Frontends: Vite dev servers on the host ──────────────────────────────
// Fixed host ports: the frontends link to each other, the consent redirect
// derives the portal's port as the consumer mock's port + 1, and the
// landing page shows the URLs.
const int LandingPort = 9000, DvMockPort = 9001, ConsentPortalPort = DvMockPort + 1, DevPortalPort = 9003, KeyperPort = 9004;
var allowedHosts = string.Join(",", new[] { publicHost, builder.Configuration["Gbo:ExtraAllowedHosts"] ?? "" }.Where(h => h.Length > 0));

IResourceBuilder<ViteAppResource> Frontend(string name, int port)
{
    return builder.AddViteApp(name, $"../{name}")
        // npm ci: install exactly the lockfile, never rewrite it.
        .WithNpm(install: true, installCommand: "ci")
        .WithEndpoint("http", e => { e.Port = port; })
        .WithEnvironment("__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS", allowedHosts);
}

var developerPortal = Frontend("developer-portal", DevPortalPort)
    .WithEnvironment("DEV_API_TARGET", devPortalBackend.GetEndpoint("http"))
    .WithEnvironment("JAEGER_API_TARGET", jaeger.GetEndpoint("http"))
    .WithEnvironment("BD_GRAPHQL_TARGET", graphqlServer.GetEndpoint("http"))
    .WithEnvironment("BRP_GRAPHQL_TARGET", brpGraphqlServer.GetEndpoint("http"))
    .WithEnvironment("VITE_JAEGER_PUBLIC_URL", jaeger.GetEndpoint("http"))
    .WithEnvironment("VITE_EUDI_UL_BASE", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance")
    .WaitFor(devPortalBackend);
// The portal proxies to whichever backends the profiles start.
foreach (var (variable, target) in new[]
{
    ("PORTAL_API_TARGET", consentPortalBackend),
    ("DVTP_API_TARGET", dienstverlenerBackend),
    ("IR_API_TARGET", irBackend),
    ("LVG_GRAPHQL_TARGET", lvgGraphqlServer),
})
{
    if (target is not null)
    {
        developerPortal.WithEnvironment(variable, target.GetEndpoint("http"));
    }
}

var landingPage = Frontend("landing-page", LandingPort)
    .WithEnvironment("VITE_DIENSTVERLENER_URL", PublicUrl(DvMockPort))
    .WithEnvironment("VITE_INSTALLATIE_REGISTER_URL", PublicUrl(KeyperPort))
    .WithEnvironment("VITE_TOESTEMMINGSPORTAAL_URL", PublicUrl(ConsentPortalPort))
    .WithEnvironment("VITE_DEVELOPER_PORTAL_URL", PublicUrl(DevPortalPort))
    .WithEnvironment("VITE_EUDI_UL_BASE", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance")
    .WithEnvironment("VITE_JAEGER_PUBLIC_URL", jaeger.GetEndpoint("http"))
    .WithEnvironment("VITE_FSC_CONTROLLER_BRON_URL", PublicUrl(8095))
    .WithEnvironment("VITE_FSC_CONTROLLER_DVTP_URL", PublicUrl(8096))
    .WithEnvironment("VITE_FSC_CONTROLLER_EUDI_URL", PublicUrl(8094))
    .WithEnvironment("VITE_VERSION_STRING", builder.Configuration["Gbo:VersionString"] ?? "");

if (On("dvtp"))
{
    Frontend("dienstverlener-mock", DvMockPort)
        .WithEnvironment("DV_API_TARGET", dienstverlenerBackend!.GetEndpoint("http"))
        .WithEnvironment("VITE_CONSUMER_PEER_ID", "99999999900000000300")
        .WaitFor(dienstverlenerBackend);
    Frontend("toestemmingsportaal-frontend", ConsentPortalPort)
        .WithEnvironment("PORTAL_API_TARGET", consentPortalBackend!.GetEndpoint("http"))
        .WithEnvironment("VITE_ALLOWED_RETURN_ORIGINS", $"{PublicUrl(DvMockPort)},{PublicUrl(KeyperPort)}")
        .WaitFor(consentPortalBackend);
    Frontend("keyper-mock", KeyperPort)
        .WithEnvironment("IR_API_TARGET", irBackend!.GetEndpoint("http"))
        .WithEnvironment("VITE_CONSUMER_PEER_ID", "99999999900000001000")
        .WithEnvironment("VITE_CONSENT_PORTAL_URL", PublicUrl(ConsentPortalPort))
        .WaitFor(irBackend);
}

// The EUDI adapter and the public EUDI URL, when the eudi profile runs.
if (eudiAdapter is not null)
{
    developerPortal
        .WithEnvironment("EUDI_API_TARGET", eudiAdapter.GetEndpoint("http"))
        .WithEnvironment("VITE_EUDI_PUBLIC_URL", eudiPublicUrl!);
    landingPage
        .WithEnvironment("EUDI_API_TARGET", eudiAdapter.GetEndpoint("http"))
        .WithEnvironment("VITE_EUDI_PUBLIC_URL", eudiPublicUrl!)
        .WaitFor(eudiAdapter);
}
if (builder.Configuration["Gbo:IssuerPublicUrl"] is { Length: > 0 } ownIssuer)
{
    developerPortal.WithEnvironment("VITE_OWN_ISSUER_PUBLIC_URL", ownIssuer);
}

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
