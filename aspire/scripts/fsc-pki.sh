#!/usr/bin/env bash
# Runs an fsc-infra/pki bootstrap script against the AppHost's certportal.
#
#   aspire/scripts/fsc-pki.sh fsc-infra/pki/bootstrap-peer.sh bd-mock
#
# The bootstrap script requests the group certificates from certportal with
# `docker run --network "$FSC_INFRA_NETWORK" … curl http://certportal:8080`.
# Under the AppHost the container network has a name of its own per run, so
# this finds it from the running certportal container, by the compose labels
# apphost.go puts on every container, and hands it to the script. The script
# itself is unchanged and skips a party whose certificates exist.
set -euo pipefail

cd "$(dirname "$0")/../.."

id=$(docker ps -q \
  --filter label=com.docker.compose.project=gbo-demo-aspire \
  --filter label=com.docker.compose.service=certportal | head -1)
if [ -z "$id" ]; then
  echo "certportal is not running under the AppHost" >&2
  exit 1
fi
FSC_INFRA_NETWORK=$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$id")
export FSC_INFRA_NETWORK

# certportal answers once cfssl does; give both a moment on a cold start.
for _ in $(seq 1 30); do
  if docker run --rm --network "$FSC_INFRA_NETWORK" gbo-demo/pki-tools:local \
      curl -fsS -o /dev/null http://certportal:8080/ 2>/dev/null; then
    break
  fi
  sleep 1
done

exec bash "$@"
