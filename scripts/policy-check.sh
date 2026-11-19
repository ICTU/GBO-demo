#!/usr/bin/env bash
# Checks that policies/ loads, the way the PDP and CI load it.
#
#   scripts/policy-check.sh          structure + `opa check` (a second or two)
#   scripts/policy-check.sh --test   also `opa fmt --diff` and `opa test`
#
# The PDP hot-reloads ./policies, but a module that does not compile is
# swallowed: OpenFTV logs `policy added/replaced` for it all the same, stays
# healthy and keeps answering with the rules it had before (#151). This
# reports the error with file and line, without the stack running. Run it
# after every edit; the AppHost runs it before it starts the PDP.
#
# Same image and checks as the rego job in .github/workflows/ci.yml: bump the
# digest in both places together.
set -euo pipefail

cd "$(dirname "$0")/.."

OPA_IMAGE="${OPA_IMAGE:-openpolicyagent/opa:1.9.0-static@sha256:60b6af32b58377718546ac7d4634eecbfe50ec36f7d3ca3f8ebf515f9826c2ac}"
# The consent tests sign and verify real ES256 tokens and evaluate the
# decision for every field of the request, which takes several seconds per
# test when they run in parallel: above OPA's default limit of 5s per test.
OPA_TEST_TIMEOUT="${OPA_TEST_TIMEOUT:-30s}"
opa() { docker run --rm -v "$PWD/policies:/src/policies:ro" -w /src "$OPA_IMAGE" "$@"; }

# The PAP loads every file under policies/ as Rego, and one that does not
# parse drops every policy after it in walk order.
stray=$(find policies -type f ! -name '*.rego' -print)
if [ -n "$stray" ]; then
  echo "non-Rego files under policies/ (move them elsewhere):" >&2
  echo "$stray" >&2
  exit 1
fi
# OpenFTV keys policies by file name.
dupes=$(find policies -name '*.rego' -exec basename {} \; | sort | uniq -d)
if [ -n "$dupes" ]; then
  echo "Rego file names used twice under policies/ (OpenFTV keys policies by file name):" >&2
  echo "$dupes" >&2
  exit 1
fi

opa check policies
echo "-> policies/ compiles"

if [ "${1:-}" = "--test" ]; then
  opa fmt --diff policies
  opa test policies -v --timeout "$OPA_TEST_TIMEOUT"
fi
