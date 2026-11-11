#!/usr/bin/env python3
"""Build the explicitly local GBO LoTE SDK patches; never publish artifacts."""
import argparse
import json
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]
SOURCES = {
    "etsi-1196x2": ("eudi-lib-kmp-etsi-1196x2", "v0.4.0-alpha.4", "b8a6cecd80f42a787f9066a27c5ef6cfcad06773", "etsi-gbo-profile.patch"),
    "android-wallet-core": ("eudi-lib-android-wallet-core", "v0.31.1", "832be7f0c787ef1b720856ff5133400f9b720409", "android-sdk-gbo-profile.patch"),
    "eudi-lib-ios-iso18013-security": ("eudi-lib-ios-iso18013-security", "v0.27.2", "68ba43c3c62355797e3d3219aadd07a8962fdc06", "ios-sdk-gbo-profile.patch"),
    "eudi-lib-ios-wallet-kit": ("eudi-lib-ios-wallet-kit", "0.54.5-gbo.1", "4d269c2e52142190b203dc45328d7ffeb0e904e9", "ios-walletkit-cache.patch"),
}


def run(*args, cwd=None):
    subprocess.run([str(a) for a in args], cwd=cwd, check=True)


def prepare(name, output, source=None):
    repo, tag, revision, patch = SOURCES[name]
    target = output / name
    if not target.exists():
        url = str(source.resolve()) if source else f"https://github.com/eu-digital-identity-wallet/{repo}.git"
        run("git", "clone", "--depth", "1", "--branch", tag, url, target)
    head = subprocess.check_output(["git", "-C", str(target), "rev-parse", "HEAD"], text=True).strip()
    if head != revision:
        raise SystemExit(f"Refusing to change {target}: expected {revision}, found {head}")
    patch = ROOT / "wallet-patches" / patch
    applied = subprocess.run(["git", "-C", str(target), "apply", "--reverse", "--check", str(patch)], capture_output=True)
    if applied.returncode:
        run("git", "-C", target, "apply", "--check", patch)
        run("git", "-C", target, "apply", patch)
    return target


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--android", action="store_true")
    parser.add_argument("--ios", action="store_true")
    parser.add_argument("--output", type=Path, default=ROOT / ".local/dependencies")
    parser.add_argument("--walletkit-source", type=Path, help="Local existing wallet-kit fork with tag 0.54.5-gbo.1")
    options = parser.parse_args()
    if not (options.android or options.ios):
        parser.error("Select --android and/or --ios")
    output = options.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    etsi = prepare("etsi-1196x2", output)
    gradle = etsi / "gradlew"
    environment = {}
    if options.android:
        core = prepare("android-wallet-core", output)
        run(gradle, ":etsi-119602-consultation:jvmJar",
            ":etsi-119602-consultation:generatePomFileForJvmPublication",
            ":etsi-119602-consultation:jvmTest", "--tests", "*GboCacheTest*", "--console=plain", cwd=etsi)
        maven = output.parent / "wallet-maven"
        artifact = "etsi-119602-consultation-jvm"
        version = "0.4.0-alpha.4-gbo.1"
        destination = maven / "eu/europa/ec/eudi" / artifact / version
        destination.mkdir(parents=True, exist_ok=True)
        build = etsi / "119602-consultation/build"
        shutil.copyfile(build / f"libs/{artifact}-0.4.0-alpha.4.jar", destination / f"{artifact}-{version}.jar")
        pom = (build / "publications/jvm/pom-default.xml").read_text()
        pom = pom.replace("<version>0.4.0-alpha.4</version>", f"<version>{version}</version>", 1)
        (destination / f"{artifact}-{version}.pom").write_text(pom)
        environment.update(GBO_WALLET_CORE_PATH=str(core), GBO_LOTE_MAVEN=str(maven))
    if options.ios:
        if not options.walletkit_source and not (output / "eudi-lib-ios-wallet-kit").exists():
            parser.error("--ios needs --walletkit-source while the existing GBO fork tag is unpublished")
        walletkit = prepare("eudi-lib-ios-wallet-kit", output, options.walletkit_source)
        security = prepare("eudi-lib-ios-iso18013-security", output)
        run(gradle, ":etsi-1196x2-ios:linkReleaseFrameworkIosArm64",
            ":etsi-1196x2-ios:linkReleaseFrameworkIosSimulatorArm64", "--console=plain", cwd=etsi)
        framework = security / "EudiEtsi1196x2.xcframework"
        temporary = output / "EudiEtsi1196x2.next.xcframework"
        if temporary.exists():
            shutil.rmtree(temporary)
        run("xcodebuild", "-create-xcframework",
            "-framework", etsi / "ios/build/bin/iosArm64/releaseFramework/EudiEtsi1196x2.framework",
            "-framework", etsi / "ios/build/bin/iosSimulatorArm64/releaseFramework/EudiEtsi1196x2.framework",
            "-output", temporary)
        if framework.exists():
            shutil.rmtree(framework)
        temporary.rename(framework)
        environment["GBO_SECURITY_PACKAGE_PATH"] = str(security)
        environment["GBO_WALLET_KIT_PACKAGE_PATH"] = str(walletkit)
    env_file = output / "gbo-sdk-environment.json"
    previous = json.loads(env_file.read_text()) if env_file.exists() else {}
    previous.update(environment)
    env_file.write_text(json.dumps(previous, indent=2) + "\n")
    print(f"Local SDK environment: {env_file}")


if __name__ == "__main__":
    main()
