#!/usr/bin/env bash
# Wipes the local FSC network: its certificates, contracts and databases.
# Stop the AppHost first. The next `aspire run` bootstraps it from scratch.
#
#   aspire/scripts/fsc-clean.sh
#
# The certificates are written by containers as root or uid 1001, which the
# host user cannot delete without sudo under rootful or rootless Docker, so
# they are deleted from a container of the pki-tools image that wrote them.
set -euo pipefail

cd "$(dirname "$0")/../.."

files=(
  fsc-infra/pki/ca/*.pem fsc-infra/pki/ca/*.csr
  fsc-infra/directory-peer/pki/org/*.pem fsc-infra/directory-peer/pki/internal/*.pem
  fsc-infra/directory-ui/pki/org/*.pem
  fsc-infra/orgs/*/pki/org/*.pem fsc-infra/orgs/*/pki/internal/*.pem
  services/openftv-pdp/certs/*.pem
)
existing=()
for f in "${files[@]}"; do
  [ -e "$f" ] && existing+=("$f")
done
if [ "${#existing[@]}" -gt 0 ]; then
  docker run --rm --pull=never -v "$PWD:/work" -w /work gbo-demo/pki-tools:local rm -f "${existing[@]}"
  echo "-> removed ${#existing[@]} certificate files"
fi

# Contracts and peer state live in the FSC Postgres volume.
docker volume rm gbo-fsc-postgres-data >/dev/null 2>&1 && echo "-> removed the FSC database volume" || true
