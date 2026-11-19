#!/usr/bin/env bash
# Builds an image from the nl-wallet sources, unless an image built from the
# same nl-wallet revision exists.
#
#   aspire/scripts/nlwallet-image.sh gbo/eudi-issuance-server:dev services/eudi-issuance-server/Dockerfile
#
# The issuance server and the ASI wallet verifier have no published image.
# The build is expensive, so an existing image is reused, but only while it
# was built from the sources now on disk: the nl-wallet revision is stamped
# on the image as a label and compared here. Bumping the submodule (or
# editing an overridden checkout) rebuilds instead of silently running the
# previous pin's binary against this pin's config. Server and wallet app
# move in lockstep; a stale binary here is a broken flow.
set -euo pipefail

cd "$(dirname "$0")/../.."

image="$1"
dockerfile="$2"
nlwallet="${NLWALLET_PATH:-$PWD/vendor/nl-wallet}"

if [ ! -f "$nlwallet/wallet_core/Cargo.toml" ]; then
  echo "ERROR: nl-wallet sources not found at $nlwallet" >&2
  echo "       Run: git submodule update --init vendor/nl-wallet" >&2
  exit 1
fi

rev="$(git -C "$nlwallet" describe --tags --always --dirty 2>/dev/null || echo unknown)"
built="$(docker image inspect "$image" --format '{{index .Config.Labels "gbo.nlwallet-rev"}}' 2>/dev/null || true)"
if [ -n "$built" ] && [ "$built" = "$rev" ]; then
  echo "-> $image is current (nl-wallet $rev)"
  exit 0
fi
if [ -n "$built" ]; then
  echo "-> Rebuilding $image: image holds nl-wallet $built, checkout is $rev"
else
  echo "-> Building $image from $nlwallet (nl-wallet $rev)"
fi
docker build -t "$image" --label gbo.nlwallet-rev="$rev" -f "$dockerfile" "$nlwallet"
