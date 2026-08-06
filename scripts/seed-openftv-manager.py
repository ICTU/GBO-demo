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
    scripts/seed-openftv-manager.py [--url http://localhost:9280] [--dry-run]
"""

from __future__ import annotations

import argparse
import json
import pathlib
import sys
import urllib.error
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


def request(method: str, url: str, body: dict | None = None) -> tuple[int, bytes]:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        url, data=data, method=method, headers={"Content-Type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()


def retire_stale(base: str, keep: set[str], dry_run: bool) -> int:
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
    status, out = request("GET", f"{base}/v1/policies")
    if status != 200:
        print(f"  could not list policies (HTTP {status}) — skipping prune", file=sys.stderr)
        return 0

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
        st, body = request("GET", f"{base}/v1/policy/{pid}")
        data = json.loads(body).get("data", "") if st == 200 else ""
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
        st, out = request("PUT", f"{base}/v1/policy/{pid}", payload)
        if st not in (200, 201, 204):
            print(f"  FAIL retiring {title}: HTTP {st} {out[:200]!r}", file=sys.stderr)
            failures += 1
        else:
            print(f"  retired {title}")

    return failures


def seed(base: str, dry_run: bool) -> int:
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
        status, out = request("POST", f"{base}/v1/policy/{pid}", payload)
        if status in (409, 400, 422):
            status, out = request("PUT", f"{base}/v1/policy/{pid}", payload)

        if status not in (200, 201, 204):
            print(f"  FAIL {rel}: HTTP {status} {out[:200]!r}", file=sys.stderr)
            failures += 1
        else:
            print(f"  seeded {rel}  (HTTP {status})")

    failures += retire_stale(base, seeded, dry_run)
    return 1 if failures else 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--url",
        default="http://localhost:9280",
        help="Manager management API (default: the compose-published port)",
    )
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    base = args.url.rstrip("/")
    print(f"seeding OpenFTV Manager at {base}")
    return seed(base, args.dry_run)


if __name__ == "__main__":
    sys.exit(main())
