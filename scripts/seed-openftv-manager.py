#!/usr/bin/env python3
"""Seed the OpenFTV Manager with the demo's Rego policies.

The Manager keeps its policies in Postgres, not on disk: its PAP is
created without a file store (apps/manager/server/pap.go says so in as
many words), so there is no directory to point it at. Policies get in
through the API — which is also how the management interface writes them,
so seeding this way exercises the same path an operator would use.

Git stays the source of truth for the demo. This script pushes what is in
policies/ into the Manager; from there the Manager bundles it and ships it
to every PDP listed in services/openftv-manager/bundles/*.yaml.

Ids are uuid5 of the file's repo-relative path, so re-running updates a
policy in place instead of creating a duplicate — the script is safe to
run on every `make` invocation.

Usage:
    FTV_MANAGER_DEPLOY_PASSWORD=... scripts/seed-openftv-manager.py \
        [--url http://localhost:9280] [--dry-run]
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid

REPO = pathlib.Path(__file__).resolve().parent.parent
POLICY_DIR = REPO / "policies"

# Everything the PDP evaluates carries this tag; the bundle config selects
# on it (services/openftv-manager/bundles/gbo-pdp.yaml).
TAG = "gbo-authz"

# Policies that once existed but no longer do keep this tag instead, since
# they cannot be deleted (see retire_stale).
RETIRED_TAG = "gbo-retired"

# uuid5 namespace so ids are stable across runs and across machines.
NS = uuid.uuid5(uuid.NAMESPACE_URL, "https://github.com/ICTU/GBO-demo/policies")


def policy_files() -> list[pathlib.Path]:
    """Every .rego file except the tests, which the PDP has no use for."""
    return sorted(
        p for p in POLICY_DIR.rglob("*.rego") if not p.name.endswith("_test.rego")
    )


def title_for(rel: pathlib.Path) -> str:
    """A human-readable title for the management interface's policy list."""
    stem = rel.as_posix().removesuffix(".rego")
    return stem.replace("/", " · ")


def request(
    method: str, url: str, token: str, body: dict | None = None
) -> tuple[int, bytes]:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        url,
        data=data,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()


def public_authority() -> str:
    """The issuer the Manager accepts (MANAGER_OIDC_ISSUER in docker-compose.yml).

    Empty variables count as unset, as in Compose.
    """
    scheme = os.environ.get("GBO_PUBLIC_SCHEME") or "http"
    host = os.environ.get("GBO_PUBLIC_HOST") or "localhost"
    port = os.environ.get("GBO_PORT_KEYCLOAK") or "9284"
    return f"{scheme}://{host}:{port}/realms/gbo"


def forwarded_headers(authority: str) -> dict[str, str]:
    """Headers that make Keycloak issue `authority` as the token's issuer.

    Keycloak runs with KC_PROXY_HEADERS=xforwarded and derives its issuer
    from these, as it does behind the reverse proxy the browser uses.
    """
    url = urllib.parse.urlsplit(authority)
    port = url.port or (443 if url.scheme == "https" else 80)
    return {
        "X-Forwarded-Proto": url.scheme,
        "X-Forwarded-Host": url.hostname or "localhost",
        "X-Forwarded-Port": str(port),
    }


def token_realm(explicit: str | None) -> tuple[str, dict[str, str]]:
    """The realm URL to ask for the deployment token, and the headers to send.

    An explicit realm is asked directly. Otherwise the demo's own Keycloak
    is asked on its published port, which is always reachable from this
    machine (the public URL may not be), with forwarded headers so the
    token still carries the public issuer the Manager accepts.
    """
    if explicit:
        return explicit, {}
    port = os.environ.get("GBO_PORT_KEYCLOAK") or "9284"
    return f"http://localhost:{port}/realms/gbo", forwarded_headers(public_authority())


def deployment_token(
    realm: str,
    headers: dict[str, str],
    client_id: str,
    username: str,
    password: str,
) -> str:
    """Get a short-lived token for the internal deployment identity."""
    form = urllib.parse.urlencode(
        {
            "grant_type": "password",
            "client_id": client_id,
            "username": username,
            "password": password,
        }
    ).encode()
    req = urllib.request.Request(
        f"{realm.rstrip('/')}/protocol/openid-connect/token",
        data=form,
        method="POST",
        headers={"Content-Type": "application/x-www-form-urlencoded", **headers},
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            payload = json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        detail = exc.read()[:200].decode(errors="replace")
        raise RuntimeError(f"OIDC token request failed: HTTP {exc.code} {detail}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"OIDC token request failed: {exc.reason}") from exc

    token = payload.get("access_token")
    if not token:
        raise RuntimeError("OIDC token response did not contain access_token")
    return token


def retire_stale(base: str, keep: set[str], dry_run: bool, token: str) -> int:
    """Drop policies the Manager still holds but git no longer has.

    Retire rather than delete: `DELETE /v1/policy/:id` fails with a foreign
    key violation on policy_audit (upstream bug, see ICTU-37), so a policy
    can never actually be removed. Stripping the bundle's tag is equivalent
    for our purpose — eam/bundles/bundle.go filters on the tag, so an
    untagged policy stops being shipped.

    This matters more than it looks. The Manager's store is authoritative
    and cumulative: rename a .rego file and the old one keeps being
    bundled, and two policies declaring the same package make the PDP fail
    to compile the whole set — every request then 500s. Exactly that
    happened while building this.
    """
    status, out = request("GET", f"{base}/v1/policies", token)
    if status != 200:
        print(f"  could not list policies (HTTP {status})", file=sys.stderr)
        return 1

    failures = 0
    for pol in json.loads(out):
        pid = pol["id"]
        tags = (pol.get("metadata") or {}).get("tags") or []
        if pid in keep or TAG not in tags:
            continue

        title = (pol.get("metadata") or {}).get("title", pid)
        if dry_run:
            print(f"  would retire {title} ({pid})")
            continue

        # Read the full record: the list view omits `data`, and PUT needs it.
        st, body = request("GET", f"{base}/v1/policy/{pid}", token)
        data = json.loads(body).get("data") if st == 200 else None
        if not data:
            # Never PUT it back without its source.
            print(f"  FAIL retiring {title}: could not read its source (HTTP {st})", file=sys.stderr)
            failures += 1
            continue
        payload = {
            "id": pid,
            "language": pol.get("language", "rego"),
            "data": data,
            "metadata": {
                "title": f"RETIRED · {title}",
                "description": "No longer present in policies/; untagged so it drops out of the bundle.",
                "tags": [RETIRED_TAG],
            },
        }
        st, out = request("PUT", f"{base}/v1/policy/{pid}", token, payload)
        if st not in (200, 201, 204):
            print(f"  FAIL retiring {title}: HTTP {st} {out[:200]!r}", file=sys.stderr)
            failures += 1
        else:
            print(f"  retired {title}")

    return failures


def seed(base: str, dry_run: bool, token: str) -> int:
    files = policy_files()
    if not files:
        print("no policies found — is policies/ present?", file=sys.stderr)
        return 1

    failures = 0
    seeded: set[str] = set()
    for path in files:
        rel = path.relative_to(REPO)
        pid = str(uuid.uuid5(NS, rel.as_posix()))
        seeded.add(pid)
        payload = {
            "id": pid,
            "language": "rego",
            "data": path.read_text(),
            "metadata": {
                "title": title_for(path.relative_to(POLICY_DIR)),
                "description": f"Seeded from {rel.as_posix()}",
                "tags": [TAG],
            },
        }

        if dry_run:
            print(f"  would seed {rel}  ({pid})")
            continue

        # POST creates; on a re-run the id already exists, so fall back to
        # PUT, which is the update verb.
        status, out = request("POST", f"{base}/v1/policy/{pid}", token, payload)
        if status in (409, 400, 422):
            status, out = request("PUT", f"{base}/v1/policy/{pid}", token, payload)

        if status not in (200, 201, 204):
            print(f"  FAIL {rel}: HTTP {status} {out[:200]!r}", file=sys.stderr)
            failures += 1
        else:
            print(f"  seeded {rel}  (HTTP {status})")

    failures += retire_stale(base, seeded, dry_run, token)
    return 1 if failures else 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--url",
        default="http://localhost:9280",
        help="Manager management API (default: the compose-published port)",
    )
    ap.add_argument(
        "--oidc-authority",
        help="OIDC realm used for the deployment identity (default: the "
        "issuer the Manager accepts, as docker-compose.yml derives it, "
        "requested from the demo's own Keycloak on localhost)",
    )
    ap.add_argument("--oidc-client-id", default="openftv-manager-ui")
    ap.add_argument("--username", default="deployment")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    base = args.url.rstrip("/")
    print(f"seeding OpenFTV Manager at {base}")
    if args.dry_run:
        return seed(base, True, "dry-run")

    password = os.environ.get("FTV_MANAGER_DEPLOY_PASSWORD", "")
    if not password:
        print("FTV_MANAGER_DEPLOY_PASSWORD is required", file=sys.stderr)
        return 1

    realm, headers = token_realm(
        args.oidc_authority or os.environ.get("GBO_FTV_OIDC_AUTHORITY")
    )
    try:
        token = deployment_token(
            realm, headers, args.oidc_client_id, args.username, password
        )
    except RuntimeError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return seed(base, False, token)


if __name__ == "__main__":
    sys.exit(main())
