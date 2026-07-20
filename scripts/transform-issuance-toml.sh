#!/usr/bin/env bash
# Post-processes a fresh issuance_server.toml (as emitted by nl-wallet's
# config-generator) so it fits this compose layout:
#
#   1. Rewrite publish_dir to /tsl-publish (compose mounts a named
#      volume there; nl-wallet hardcodes an absolute host path).
#   2. Triplicate the [disclosure_settings.inkomensverklaring…] block
#      into _2023 / _2024 / _2025 variants. Compose overrides only the
#      per-year base_url env-var; the trust_anchors + credential-spec
#      must already be present per year or issuance-server refuses to
#      start with "missing configuration field ... .trust_anchors".
#
# Idempotent: running twice is a no-op (already-triplicated blocks are
# detected and skipped).

set -euo pipefail

target="${1:?usage: transform-issuance-toml.sh <path/to/issuance_server.toml>}"

if [ ! -f "$target" ]; then
  echo "transform-issuance-toml: file not found: $target" >&2
  exit 1
fi

# 1. publish_dir → container path.
if grep -qE '^publish_dir\s*=' "$target"; then
  sed -i.bak -E 's|^publish_dir\s*=.*|publish_dir = "/tsl-publish"|' "$target"
  rm -f "$target.bak"
fi

# 2. Triplicate disclosure_settings.inkomensverklaring → _2023/_2024/_2025.
python3 - "$target" <<'PY'
import re, sys, pathlib
p = pathlib.Path(sys.argv[1])
src = p.read_text()

# Skip if already triplicated (idempotency guard).
if all(f'disclosure_settings.inkomensverklaring_{y}' in src for y in (2023, 2024, 2025)):
    sys.exit(0)

lines = src.splitlines(keepends=True)
SEC = re.compile(r'^\s*\[\[?\s*([^\[\]]+?)\s*\]\]?\s*$')

start = end = None
for i, line in enumerate(lines):
    m = SEC.match(line)
    if not m:
        continue
    name = m.group(1)
    if name.startswith('disclosure_settings.inkomensverklaring'):
        if start is None:
            start = i
    elif start is not None:
        end = i - 1
        break

if end is None and start is not None:
    end = len(lines) - 1

if start is None:
    sys.exit(0)   # nothing to triplicate

block = ''.join(lines[start:end + 1])

def variant(block, year):
    return re.sub(
        r'disclosure_settings\.inkomensverklaring(?=[.\]])',
        f'disclosure_settings.inkomensverklaring_{year}',
        block,
    )

triplicated = ''.join(variant(block, y) for y in (2023, 2024, 2025))
new_src = ''.join(lines[:start]) + triplicated + ''.join(lines[end + 1:])
p.write_text(new_src)
PY
