#!/usr/bin/env python3
"""Apply KEY=VALUE assignments to a .env file, in place and idempotently.

Written for the EUDI certificate slots, whose values are single-line base64
tens of kilobytes long. Editing those by hand is error-prone in exactly the
ways that cost the most time: appending a fresh block leaves the previous
assignment above it (last-wins hides the mistake until something reads the
file differently), and hand-deleting the old lines is one mis-selected line
away from dropping a slot entirely — which then falls back silently, or
pairs a new certificate with an old private key.

So: every key is replaced where it already sits, any later duplicates of it
are removed, and unknown keys are appended. Comments, blank lines, ordering
and unrelated keys are left untouched. A timestamped backup is written
before anything changes.

Values are never printed — only key names, byte lengths and what happened.

Read assignments from one or more files (the output of mint-eudi-certs.py):

  python3 scripts/update-env.py readers.env

Or pass them literally, or mix both:

  python3 scripts/update-env.py readers.env --set EUDI_CLIENT_ID=eudi-is.example.nl

Preview without writing:

  python3 scripts/update-env.py readers.env --dry-run
"""
import argparse, datetime, pathlib, shutil, sys


def parse_assignments(text, source):
    """KEY=VALUE per line; blank lines and # comments ignored."""
    out = []
    for n, line in enumerate(text.splitlines(), 1):
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if "=" not in stripped:
            sys.exit(f"{source}:{n}: not a KEY=VALUE assignment: {stripped[:60]!r}")
        key, value = stripped.split("=", 1)
        key = key.strip()
        if not key or any(c.isspace() for c in key):
            sys.exit(f"{source}:{n}: implausible key {key[:60]!r}")
        out.append((key, value))
    return out


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("files", nargs="*", help="files containing KEY=VALUE lines")
    p.add_argument("--set", action="append", default=[], metavar="KEY=VALUE",
                   help="a literal assignment; repeatable, applied after the files")
    p.add_argument("--env", default=".env", help="target file (default: .env)")
    p.add_argument("--dry-run", action="store_true", help="report what would change, write nothing")
    p.add_argument("--no-backup", action="store_true", help="skip the timestamped backup (not advised)")
    a = p.parse_args()

    env_path = pathlib.Path(a.env)
    if not env_path.is_file():
        sys.exit(f"{env_path}: no such file")

    assignments = []
    for f in a.files:
        path = pathlib.Path(f)
        if not path.is_file():
            sys.exit(f"{path}: no such file")
        assignments += parse_assignments(path.read_text(), path)
    assignments += parse_assignments("\n".join(a.set), "--set")

    if not assignments:
        sys.exit("nothing to apply: pass a file of KEY=VALUE lines and/or --set KEY=VALUE")

    # Later assignments of the same key win, so a --set can override a file.
    wanted = {}
    for key, value in assignments:
        wanted[key] = value

    original = env_path.read_text()
    lines = original.splitlines()
    ends_with_newline = original.endswith("\n")

    seen = set()
    kept, removed = [], []
    report = {}

    for line in lines:
        stripped = line.lstrip()
        key = stripped.split("=", 1)[0].strip() if "=" in stripped and not stripped.startswith("#") else None

        if key is None or key not in wanted:
            kept.append(line)
            continue

        old_len = len(line.split("=", 1)[1])
        if key in seen:
            # A duplicate assignment further down the file: drop it.
            removed.append((key, old_len))
            continue

        seen.add(key)
        kept.append(f"{key}={wanted[key]}")
        report[key] = ("replaced", old_len, len(wanted[key]))

    appended = [k for k in wanted if k not in seen]
    for key in appended:
        kept.append(f"{key}={wanted[key]}")
        report[key] = ("appended", None, len(wanted[key]))

    width = max(len(k) for k in wanted)
    for key in wanted:
        action, old_len, new_len = report[key]
        before = f"{old_len} bytes" if old_len is not None else "absent"
        print(f"  {action:<9} {key:<{width}}  {before:>12} -> {new_len} bytes")
    for key, old_len in removed:
        print(f"  {'deduped':<9} {key:<{width}}  {old_len:>6} bytes -> removed (later duplicate)")

    if a.dry_run:
        print(f"\ndry run: {env_path} unchanged")
        return

    if not a.no_backup:
        stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
        backup = env_path.with_name(f"{env_path.name}.{stamp}.bak")
        shutil.copy2(env_path, backup)
        print(f"\nbackup: {backup}")

    env_path.write_text("\n".join(kept) + ("\n" if ends_with_newline else ""))
    print(f"wrote:  {env_path}  ({len(wanted)} keys set, {len(removed)} duplicates removed)")
    print("next:   make eudi-config && docker compose --profile eudi restart eudi-issuance-server")


if __name__ == "__main__":
    main()
