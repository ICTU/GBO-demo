#:package Aspire.Hosting.AppHost@17.0.0
#:package Aspire.Hosting.Dotnet@17.0.0
#:package Aspire.Hosting.Go@17.0.0

// Phase 0 spike: two Go services of GBO-demo under a C# file-based AppHost.
// See README.md in this directory for how to run it and the test results.

var builder = DistributedApplication.CreateBuilder(args);

// Repository root, relative to this AppHost directory.
var repoRoot = Path.GetFullPath(Path.Combine(builder.AppHostDirectory, ".."));
var dataRoot = Path.Combine(repoRoot, ".aspire-data");
Directory.CreateDirectory(Path.Combine(dataRoot, "logboek-bd"));

// Same local-demo defaults as docker-compose.yml; override with
// Parameters__ldv-write-token etc. or `aspire secret set`.
var ldvWriteToken = builder.AddParameter("ldv-write-token", "local-demo-logboek-token", secret: true);
var ldvReadToken = builder.AddParameter("ldv-read-token", "local-demo-logboek-read-token", secret: true);
var ldvPseudonymKey = builder.AddParameter("ldv-subject-pseudonym-key", "local-demo-pseudonym-key", secret: true);

// Off by default: the services pass OTEL_EXPORTER_OTLP_ENDPOINT to
// otlptracegrpc.WithEndpoint, which wants host:port without a scheme. Set
// GboSpike:OtlpHostPortWorkaround=true to rewrite Aspire's URL (see README).
var otlpWorkaround = string.Equals(builder.Configuration["GboSpike:OtlpHostPortWorkaround"], "true", StringComparison.OrdinalIgnoreCase);

// logboek-bd: Logboek Dataverwerkingen of the Belastingdienst, run as a Go
// process with `go run .` (no container).
var logboekBd = builder.AddGoApp("logboek-bd", "../services/ldv-logboek")
    .WithHttpEndpoint(env: "PORT")
    .WithHttpHealthCheck("/health")
    .WithEnvironment("OTEL_SERVICE_NAME", "logboek-bd")
    .WithEnvironment("DATABASE_PATH", Path.Combine(dataRoot, "logboek-bd", "logboek.db"))
    .WithEnvironment("REGISTER_PATH", Path.Combine(repoRoot, "services/ldv-logboek/config/verwerkingsactiviteiten-bd.json"))
    .WithEnvironment("LDV_LOGBOOK_ID", "https://logboek.belastingdienst.nl/data-processing-operations")
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_READ_TOKEN", ldvReadToken);

// graphql-server: built from its existing Dockerfile with the repository root
// as context, like docker-compose.yml does.
var graphqlServer = builder.AddDockerfile("graphql-server", "..", "services/graphql-server/Dockerfile")
    .WithHttpEndpoint(targetPort: 4000, env: "PORT")
    .WithHttpHealthCheck("/health")
    .WithOtlpExporter()
    .WithBindMount("../services/graphql-server/config", "/config", isReadOnly: true)
    .WithVolume("gbo-spike-ldv-outbox-graphql-server", "/data")
    .WithEnvironment("OTEL_SERVICE_NAME", "graphql-server")
    .WithEnvironment("GBO_SOURCE_METADATA_PATH", "/config/gbo-source-metadata.json")
    .WithEnvironment("LDV_LOGBOOK_URL", logboekBd.GetEndpoint("http"))
    .WithEnvironment("LDV_WRITE_TOKEN", ldvWriteToken)
    .WithEnvironment("LDV_SUBJECT_PSEUDONYM_KEY", ldvPseudonymKey)
    .WithEnvironment("LDV_YEAR_ACTIVITY_TEMPLATE", "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-%d/v1")
    .WaitFor(logboekBd);

if (otlpWorkaround)
{
    // The services want "host:port" without a scheme and dial without TLS, so
    // this only works with the http launch profile. The value Aspire injects
    // is not a plain string when environment callbacks run, so it is derived
    // from the dashboard's OTLP URL instead; containers reach the host as
    // aspire.dev.internal.
    var otlpUrl = new Uri(builder.Configuration["ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL"]
        ?? throw new InvalidOperationException("ASPIRE_DASHBOARD_OTLP_ENDPOINT_URL is not set"));
    if (otlpUrl.Scheme != "http")
    {
        throw new InvalidOperationException("GboSpike:OtlpHostPortWorkaround needs the http launch profile (aspire run --launch-profile http)");
    }
    graphqlServer.WithEnvironment(context =>
    {
        context.EnvironmentVariables["OTEL_EXPORTER_OTLP_ENDPOINT"] = $"aspire.dev.internal:{otlpUrl.Port}";
    });
}

builder.Build().Run();
