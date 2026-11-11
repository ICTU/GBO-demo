#!/usr/bin/env python3
"""Build a GBO test wallet with explicit public endpoints and an independent LoTE pin."""
import argparse
import base64
import json
import os
from pathlib import Path
import re
import subprocess
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]


def https_base(value):
    parsed = urlparse(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("Expected a public HTTPS base URL without credentials/query/fragment")
    return value.rstrip("/")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("platform", choices=["ios", "android"])
    parser.add_argument("--wallet", type=Path, required=True)
    parser.add_argument("--variant", choices=["dev", "demo"], default="dev")
    parser.add_argument("--trust-url", type=https_base, required=True)
    parser.add_argument("--issuer-url", type=https_base, required=True)
    parser.add_argument("--root", type=Path, required=True, help="Independently provisioned PUBLIC LoTE root certificate, PEM or DER")
    parser.add_argument("--sdk-environment", type=Path, default=ROOT / ".local/dependencies/gbo-sdk-environment.json")
    parser.add_argument("--walletkit", type=Path, help="Optional local unpublished iOS wallet-kit fork")
    parser.add_argument("--openid4vci", type=Path, help="Optional local unpublished iOS OpenID4VCI fork")
    parser.add_argument("--output", type=Path, default=ROOT / ".local/wallet-build")
    parser.add_argument("--android-abi", choices=["arm64-v8a", "x86_64", "all"], default="arm64-v8a")
    options = parser.parse_args()
    env = os.environ.copy()
    env.update(json.loads(options.sdk_environment.read_text()))
    certificate = options.root.read_bytes()
    if b"PRIVATE KEY" in certificate:
        parser.error("Only a public certificate may enter a wallet build")
    if certificate.startswith(b"-----BEGIN CERTIFICATE-----"):
        certificate = base64.b64decode(re.sub(rb"-----[^\n]+-----|\s+", b"", certificate), validate=True)
    pin = base64.b64encode(certificate).decode("ascii")
    output = options.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if options.platform == "android":
        if options.variant != "dev":
            parser.error("The GBO Android trust configuration belongs to the dev source set; select --variant dev")
        if not env.get("GBO_WALLET_CORE_PATH") or not env.get("GBO_LOTE_MAVEN"):
            parser.error("Prepare the Android SDK first")
        command = ["./gradlew", ":app:assembleDevDebug", ":core-logic:testDevDebugUnitTest",
                   "--tests", "eu.europa.ec.corelogic.config.GboLoTEVerifierTest", "--console=plain",
                   f"-Pgbo.dev.trustBaseUrl={options.trust_url}",
                   f"-Pgbo.dev.issuerBaseUrl={options.issuer_url}", f"-Pgbo.dev.loteRootBase64={pin}"]
        if options.android_abi != "all":
            command.append(f"-Pgbo.abi={options.android_abi}")
    else:
        if not env.get("GBO_SECURITY_PACKAGE_PATH") or not env.get("GBO_WALLET_KIT_PACKAGE_PATH"):
            parser.error("Prepare the iOS SDK first")
        redirects = []
        for repo, path in [("eudi-lib-ios-wallet-kit", options.walletkit), ("eudi-lib-ios-openid4vci-swift", options.openid4vci)]:
            if path:
                redirects.append((f"url.{path.resolve().as_uri()}.insteadOf", f"https://github.com/jeroendk/{repo}.git"))
        # Per-process only: never changes global Git credentials or configuration.
        count = int(env.get("GIT_CONFIG_COUNT", "0"))
        for index, (key, value) in enumerate(redirects, count):
            env[f"GIT_CONFIG_KEY_{index}"] = key
            env[f"GIT_CONFIG_VALUE_{index}"] = value
        env["GIT_CONFIG_COUNT"] = str(count + len(redirects))
        variant = options.variant.capitalize()
        command = ["xcodebuild", "-project", "EudiReferenceWallet.xcodeproj", "-scheme", f"EUDI Wallet {variant}",
                   "-configuration", f"Debug {variant}", "-destination", "generic/platform=iOS Simulator",
                   "-derivedDataPath", str(output / "ios"), "ARCHS=arm64", "ONLY_ACTIVE_ARCH=YES",
                   "CODE_SIGNING_ALLOWED=YES", "CODE_SIGN_IDENTITY=-", f"GBO_TRUST_BASE_URL={options.trust_url}",
                   f"GBO_ISSUER_BASE_URL={options.issuer_url}", f"GBO_LOTE_ROOT_BASE64={pin}",
                   "-skipPackagePluginValidation", "-skipMacroValidation", "build"]
    log = output / f"{options.platform}-build.log"
    with log.open("w") as stream:
        result = subprocess.run(command, cwd=options.wallet.resolve(), env=env, stdout=stream, stderr=subprocess.STDOUT)
    print(f"Build {'succeeded' if result.returncode == 0 else 'failed'}; log: {log}")
    raise SystemExit(result.returncode)


if __name__ == "__main__":
    main()
