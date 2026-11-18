#:package Aspire.Hosting.AppHost@17.0.0
#:package Aspire.Hosting.Dotnet@17.0.0
#:package Aspire.Hosting.JavaScript@17.0.0

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
if (profiles.Contains("eudi") || profiles.Contains("credential-issuer") || profiles.Contains("wallet-registration")) { profiles.Add("onboarding"); }
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
var dashboardOtlp = new Uri(builder.Configuration["ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL"]
    ?? throw new InvalidOperationException("ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL is not set; start the AppHost with `aspire run`."));
if (dashboardOtlp.Scheme != "http")
{
    throw new InvalidOperationException("The collector forwards to the dashboard over plaintext gRPC; use an http launch profile.");
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

Frontend("landing-page", LandingPort)
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
