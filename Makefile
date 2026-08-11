.PHONY: up down logs clean certs fsc-local-env fsc-ca fsc-up fsc-down fsc-test fsc-clean \
        fsc-seed-bri fsc-seed-bri-hv fsc-seed-brp fsc-seed-metadata fsc-pdp-cert \
        eudi-images development-source-metadata-key source-metadata-up \
        validate-source onboard-source onboard-demo-sources onboarding-directories demo demo-minimal demo-dvtp demo-eudi \
        demo-full demo-down eudi-config

-include .env
-include fsc-infra/.env
export

# nl-wallet source for the eudi-issuance-server build. Pinned via git
# submodule (vendor/nl-wallet, v0.4.1 — the preprod wallet app rejects
# v0.5.0's scheme-prefixed client_id). Override in .env if needed.
NLWALLET_PATH ?= $(PWD)/vendor/nl-wallet

# Phase-4 filesystem onboarding. The fixed OIN and deterministic source metadata
# key are strictly demo-only; issuer/reader/status keys remain randomly
# generated below the ignored .local/ secret backend.
DEVELOPMENT_SOURCE_OIN ?= 99999999900000000200
DEVELOPMENT_BRP_SOURCE_OIN ?= 99999999900000000400
ONBOARDING_OUTWAY_URL ?= http://localhost:8087
ONBOARDING_STATE_DIR ?= $(PWD)/.local/onboarding
ONBOARDING_SECRETS_DIR ?= $(PWD)/.local/secrets
ONBOARDING_TYPE_METADATA_URL ?= $(or $(EUDI_BRI_URL),http://localhost:9409)
ONBOARDING_EUDI_ENV ?= $(ONBOARDING_SECRETS_DIR)/$(DEVELOPMENT_SOURCE_OIN)/issuance.env
ONBOARDING_ACTIVE_SOURCE ?= $(ONBOARDING_STATE_DIR)/active/$(DEVELOPMENT_SOURCE_OIN).json
ONBOARDING_BRP_EUDI_ENV ?= $(ONBOARDING_SECRETS_DIR)/$(DEVELOPMENT_BRP_SOURCE_OIN)/issuance.env
ONBOARDING_ACTIVE_BRP_SOURCE ?= $(ONBOARDING_STATE_DIR)/active/$(DEVELOPMENT_BRP_SOURCE_OIN).json
ONBOARDING_STORAGE_BACKEND ?= filesystem
ONBOARDING_CERTIFICATE_PROVIDER ?= development-ca

# Docker network of the fsc-infra instance this checkout uses. Equals
# <FSC_PROJECT_NAME>_default; override in fsc-infra/.env to run a
# per-worktree fsc-infra side by side with another checkout's.
FSC_INFRA_NETWORK ?= fsc-infra_default

up: certs
	docker compose up --build -d

down:
	docker compose down

# ── Demo bootstrap targets ───────────────────────────────────────────────
# Each target brings up one of the compose-profile combinations. Certs
# are auto-generated on first run (idempotent); subsequent runs skip.
#
#   make demo          → default DvTP flow (over real FSC via Hypotheekverlener-mock)
#   make demo-minimal  → base only: adapter/pdp/openftv-pdp/graphql + observability
#   make demo-dvtp     → alias for 'make demo'
#   make demo-eudi     → EUDI flow over real FSC (auto init + seed-bri)
#   make demo-full     → everything on (DvTP + EUDI + fsc-infra)
#   make demo-down     → everything down (main + fsc-infra)

demo: demo-dvtp

demo-minimal: certs
	@echo "-> Base stack (no profile): 13 services"
	docker compose up --build -d
	@echo ""
	@echo "  Dev-portal:    http://localhost:9003  |  http://$$(hostname -I | awk '{print $$1}'):9003"
	@echo "  Jaeger:        http://localhost:9686  |  http://$$(hostname -I | awk '{print $$1}'):9686"
	@echo "  OpenFTV PDP:   https://localhost:9181/authzen/v1/evaluation (POST)"

demo-dvtp: certs fsc-all-up fsc-seed-bri fsc-seed-bri-hv
	@echo "-> DvTP stack: base + dienstverlener + toestemmingsportaal (via real FSC)"
	docker compose --profile dvtp up --build -d
	@echo ""
	@echo "  Dev-portal:          http://localhost:9003  |  http://$$(hostname -I | awk '{print $$1}'):9003"
	@echo "  Toestemmingsportaal: http://localhost:9002  |  http://$$(hostname -I | awk '{print $$1}'):9002"
	@echo "  Dienstverlener:      http://localhost:9001  |  http://$$(hostname -I | awk '{print $$1}'):9001"
	@echo "  Jaeger:              http://localhost:9686  |  http://$$(hostname -I | awk '{print $$1}'):9686"

EUDI_CONFIG_DIR := services/eudi-issuance-server/config
EUDI_CONFIG_FILES := issuance_server.toml inkomensverklaring_metadata.json \
    akte_van_overlijden_metadata.json issuer_auth.json reader_auth.json \
    akte_van_overlijden_issuer_auth.json akte_van_overlijden_reader_auth.json
EUDI_REQUIRED_VARS := EUDI_PUBLIC_URL EUDI_READER_ORIGIN_URL EUDI_BRI_URL \
    EUDI_READER_KEY EUDI_READER_CERT \
    EUDI_ISSUER_KEY EUDI_ISSUER_CERT \
    EUDI_STATUS_KEY EUDI_STATUS_CERT

eudi-config:
	@command -v envsubst >/dev/null 2>&1 || { \
	  echo "ERROR: envsubst not found. Install with: brew install gettext"; \
	  exit 1; \
	}
	@set -a; [ -f .env ] && . ./.env; \
	  command -v jq >/dev/null 2>&1 || { echo "ERROR: jq not found"; exit 1; }; \
	  [ -f "$(ONBOARDING_EUDI_ENV)" ] || { echo "ERROR: onboarding issuance environment not found: $(ONBOARDING_EUDI_ENV)"; exit 1; }; \
	  [ -f "$(ONBOARDING_ACTIVE_SOURCE)" ] || { echo "ERROR: active source registration not found: $(ONBOARDING_ACTIVE_SOURCE)"; exit 1; }; \
	  [ -f "$(ONBOARDING_ACTIVE_BRP_SOURCE)" ] || { echo "ERROR: active BRP source registration not found: $(ONBOARDING_ACTIVE_BRP_SOURCE)"; exit 1; }; \
	  [ -f "$(ONBOARDING_BRP_EUDI_ENV)" ] || { echo "ERROR: BRP onboarding issuance environment not found: $(ONBOARDING_BRP_EUDI_ENV)"; exit 1; }; \
	  type_count=$$(jq '[.types[] | select(.type_id == "inkomensverklaring")] | length' "$(ONBOARDING_ACTIVE_SOURCE)"); \
	  [ "$$type_count" = "1" ] || { echo "ERROR: active source must contain exactly one inkomensverklaring type"; exit 1; }; \
	  EUDI_INKOMENSVERKLARING_VCT=$$(jq -er '.types[] | select(.type_id == "inkomensverklaring") | .vct' "$(ONBOARDING_ACTIVE_SOURCE)"); \
	  source_oin=$$(jq -er '.source.source_oin' "$(ONBOARDING_ACTIVE_SOURCE)"); \
	  source_type_id=$$(jq -er '.types[] | select(.type_id == "inkomensverklaring") | .type_id' "$(ONBOARDING_ACTIVE_SOURCE)"); \
	  EUDI_INKOMENSVERKLARING_ENDPOINT="$${EUDI_BRI_URL%/}/attestations/$$source_oin/$$source_type_id"; \
	  EUDI_AKTE_VCT=$$(jq -er '.types[] | select(.type_id == "akte-van-overlijden") | .vct' "$(ONBOARDING_ACTIVE_BRP_SOURCE)"); \
	  brp_source_oin=$$(jq -er '.source.source_oin' "$(ONBOARDING_ACTIVE_BRP_SOURCE)"); \
	  EUDI_AKTE_ENDPOINT="$${EUDI_BRI_URL%/}/attestations/$$brp_source_oin/akte-van-overlijden"; \
	  onboarding_brp_type_metadata=$$(jq -er '.types[] | select(.type_id == "akte-van-overlijden") | .type_metadata_reference' "$(ONBOARDING_ACTIVE_BRP_SOURCE)"); \
	  onboarding_type_metadata=$$(jq -er '.types[] | select(.type_id == "inkomensverklaring") | .type_metadata_reference' "$(ONBOARDING_ACTIVE_SOURCE)"); \
	  [ -f "$$onboarding_type_metadata" ] || { echo "ERROR: activated Type Metadata not found: $$onboarding_type_metadata"; exit 1; }; \
	  jq -e --arg vct "$$EUDI_INKOMENSVERKLARING_VCT" '.vct == $$vct' "$$onboarding_type_metadata" >/dev/null || { echo "ERROR: activated Type Metadata does not match its VCT"; exit 1; }; \
	  export EUDI_INKOMENSVERKLARING_VCT EUDI_INKOMENSVERKLARING_ENDPOINT EUDI_AKTE_VCT EUDI_AKTE_ENDPOINT; \
	  echo "-> Using onboarding certificates from $(ONBOARDING_EUDI_ENV) (overrides .env certificate values)"; \
	  . "$(ONBOARDING_EUDI_ENV)"; \
	  EUDI_BRP_ISSUER_KEY=$$(set -a; . "$(ONBOARDING_BRP_EUDI_ENV)"; printf %s "$$EUDI_ISSUER_KEY"); \
	  EUDI_BRP_ISSUER_CERT=$$(set -a; . "$(ONBOARDING_BRP_EUDI_ENV)"; printf %s "$$EUDI_ISSUER_CERT"); \
	  EUDI_BRP_READER_KEY=$$(set -a; . "$(ONBOARDING_BRP_EUDI_ENV)"; printf %s "$$EUDI_READER_KEY"); \
	  EUDI_BRP_READER_CERT=$$(set -a; . "$(ONBOARDING_BRP_EUDI_ENV)"; printf %s "$$EUDI_READER_CERT"); \
	  EUDI_BRP_STATUS_KEY=$$(set -a; . "$(ONBOARDING_BRP_EUDI_ENV)"; printf %s "$$EUDI_STATUS_KEY"); \
	  EUDI_BRP_STATUS_CERT=$$(set -a; . "$(ONBOARDING_BRP_EUDI_ENV)"; printf %s "$$EUDI_STATUS_CERT"); \
	  export EUDI_BRP_ISSUER_KEY EUDI_BRP_ISSUER_CERT EUDI_BRP_READER_KEY EUDI_BRP_READER_CERT EUDI_BRP_STATUS_KEY EUDI_BRP_STATUS_CERT; set +a; \
	missing=""; for v in $(EUDI_REQUIRED_VARS); do \
	  eval "val=\$$$$v"; \
	  [ -n "$$val" ] || missing="$$missing $$v"; \
	done; \
	if [ -n "$$missing" ]; then \
	  echo "ERROR: missing env-vars (see .env.example):$$missing"; \
	  exit 1; \
	fi; \
	for v in EUDI_BRP_ISSUER_KEY EUDI_BRP_ISSUER_CERT EUDI_BRP_READER_KEY EUDI_BRP_READER_CERT EUDI_BRP_STATUS_KEY EUDI_BRP_STATUS_CERT; do \
	  eval "val=\$$$$v"; \
	  [ -n "$$val" ] || { echo "ERROR: BRP onboarding environment is missing $$v"; exit 1; }; \
	done; \
	for f in $(EUDI_CONFIG_FILES); do \
	  if [ "$$f" = "inkomensverklaring_metadata.json" ]; then \
	    echo "-> Installing activated Type Metadata as $(EUDI_CONFIG_DIR)/$$f"; \
	    cp "$$onboarding_type_metadata" "$(EUDI_CONFIG_DIR)/$$f"; \
	  elif [ "$$f" = "akte_van_overlijden_metadata.json" ]; then \
	    echo "-> Installing activated BRP Type Metadata as $(EUDI_CONFIG_DIR)/$$f"; \
	    cp "$$onboarding_brp_type_metadata" "$(EUDI_CONFIG_DIR)/$$f"; \
	  else \
	    echo "-> Rendering $(EUDI_CONFIG_DIR)/$$f from $$f.example"; \
	    envsubst < $(EUDI_CONFIG_DIR)/$$f.example > $(EUDI_CONFIG_DIR)/$$f; \
	  fi; \
	done

# eudi-issuance-server has no published image — built from the local
# nl-wallet checkout ($NLWALLET_PATH).
eudi-images:
	@if [ ! -f "$$NLWALLET_PATH/wallet_core/Cargo.toml" ]; then \
	  echo "ERROR: nl-wallet sources not found at $$NLWALLET_PATH"; \
	  echo "       Run: git submodule update --init vendor/nl-wallet"; \
	  exit 1; \
	fi
	@if ! docker image inspect gbo/eudi-issuance-server:dev >/dev/null 2>&1; then \
	  echo "-> Building gbo/eudi-issuance-server:dev from $$NLWALLET_PATH"; \
	  docker build -t gbo/eudi-issuance-server:dev \
	    -f services/eudi-issuance-server/Dockerfile "$$NLWALLET_PATH"; \
	fi

development-source-metadata-key:
	@mkdir -p .local/secrets/source-metadata/$(DEVELOPMENT_SOURCE_OIN)
	@cd services/graphql-server && go run . init-development-metadata-key \
		--source-oin $(DEVELOPMENT_SOURCE_OIN) \
		--output $(PWD)/.local/secrets/source-metadata/$(DEVELOPMENT_SOURCE_OIN)/private.jwk
	@mkdir -p .local/secrets/source-metadata/$(DEVELOPMENT_BRP_SOURCE_OIN)
	@cd services/graphql-server && go run . init-development-metadata-key \
		--source-oin $(DEVELOPMENT_BRP_SOURCE_OIN) \
		--output $(PWD)/.local/secrets/source-metadata/$(DEVELOPMENT_BRP_SOURCE_OIN)/private.jwk

source-metadata-up: development-source-metadata-key
	GBO_ATTESTATIONS_PATH=/config/gbo-attestations.json \
	GBO_METADATA_SIGNING_JWK_PATH=/source-metadata/private.jwk \
	EUDI_PUBLIC_URL="$${EUDI_PUBLIC_URL:-http://localhost:8001}" \
	EUDI_BRI_URL="$${EUDI_BRI_URL:-http://localhost:9409}" \
	EUDI_POSTGRES_PASSWORD="$${EUDI_POSTGRES_PASSWORD:-local-not-used}" \
		docker compose up --build --force-recreate -d openftv-pdp additional-claims-service graphql-server brp-graphql-server

validate-source:
	@test -n "$(SOURCE)" || { echo "ERROR: SOURCE=sources/<oin>.yaml is required"; exit 1; }
	@cd services/eudi-adapter && go run . validate-source \
		--source "$(abspath $(SOURCE))" \
		--outway-url "$(ONBOARDING_OUTWAY_URL)" \
		--schema "$(PWD)/schemas/gbo-attestations-v1.schema.json" \
		--type-metadata-base-url "$(ONBOARDING_TYPE_METADATA_URL)" \
		--reader-public-url "$${EUDI_PUBLIC_URL:-}" \
		--reader-origin-url "$${EUDI_READER_ORIGIN_URL:-}" \
		--state-dir "$(ONBOARDING_STATE_DIR)" \
		--secrets-dir "$(ONBOARDING_SECRETS_DIR)"

onboarding-directories:
	@mkdir -p "$(ONBOARDING_STATE_DIR)/type-metadata" "$(ONBOARDING_STATE_DIR)/trust" "$(ONBOARDING_STATE_DIR)/active"

onboard-source:
	@test -n "$(SOURCE)" || { echo "ERROR: SOURCE=sources/<oin>.yaml is required"; exit 1; }
	@dry_run=""; if [ "$(DRY_RUN)" = "true" ]; then dry_run="--dry-run"; fi; \
	cd services/eudi-adapter && go run . onboard-source \
		--source "$(abspath $(SOURCE))" \
		--storage-backend "$(ONBOARDING_STORAGE_BACKEND)" \
		--certificate-provider "$(ONBOARDING_CERTIFICATE_PROVIDER)" \
		--outway-url "$(ONBOARDING_OUTWAY_URL)" \
		--schema "$(PWD)/schemas/gbo-attestations-v1.schema.json" \
		--type-metadata-base-url "$(ONBOARDING_TYPE_METADATA_URL)" \
		--reader-public-url "$${EUDI_PUBLIC_URL:-}" \
		--reader-origin-url "$${EUDI_READER_ORIGIN_URL:-}" \
		--state-dir "$(ONBOARDING_STATE_DIR)" \
		--secrets-dir "$(ONBOARDING_SECRETS_DIR)" \
		$$dry_run

# Complete, idempotent local onboarding for both demo sources. The sequence is
# explicit so a clean checkout cannot reach eudi-config before metadata has
# been published, transported through FSC, verified and activated.
onboard-demo-sources: certs
	$(MAKE) fsc-all-up
	$(MAKE) source-metadata-up
	$(MAKE) fsc-seed-metadata
	$(MAKE) onboard-source SOURCE=sources/$(DEVELOPMENT_SOURCE_OIN).yaml
	$(MAKE) onboard-source SOURCE=sources/$(DEVELOPMENT_BRP_SOURCE_OIN).yaml

demo-eudi: onboard-demo-sources fsc-seed-bri fsc-seed-brp eudi-config eudi-images
	@echo "-> EUDI stack: base + eudi branch + fsc-infra"
	docker compose --profile eudi up --build -d
	@echo ""
	@echo "  Dev-portal:      http://localhost:9003  |  http://$$(hostname -I | awk '{print $$1}'):9003"
	@echo "  EUDI-adapter:    http://localhost:9409  |  http://$$(hostname -I | awk '{print $$1}'):9409"
	@echo "  Jaeger:          http://localhost:9686  |  http://$$(hostname -I | awk '{print $$1}'):9686"

demo-full: onboard-demo-sources fsc-seed-bri fsc-seed-brp fsc-seed-bri-hv eudi-config eudi-images
	@echo "-> Full stack: everything on"
	docker compose --profile full up --build -d

demo-down:
	docker compose --profile full down
	docker compose -f fsc-infra/docker-compose.yml down

logs:
	docker compose logs -f

clean:
	docker compose down -v --rmi local

certs:
	@if [ ! -f certs/ca.pem ]; then cd certs && bash generate.sh; fi

# --- FSC-infra (productionisation) --------------------------------------
# Runs our own root-CA + certportal. Separate from the main demo stack.

fsc-local-env:
	@if [ ! -f fsc-infra/.env ]; then \
		umask 077; \
		printf 'FSC_POSTGRES_PASSWORD=%s\n' "$$(openssl rand -hex 24)" > fsc-infra/.env; \
		echo "-> Generated ignored fsc-infra/.env for local FSC"; \
	fi

fsc-ca:
	@if [ ! -f fsc-infra/pki/ca/root.pem ]; then bash fsc-infra/pki/generate-root-ca.sh; fi

fsc-up: fsc-local-env fsc-ca
	docker compose -f fsc-infra/docker-compose.yml up --build -d cfssl certportal

fsc-directory-certs: fsc-up
	@if [ ! -f fsc-infra/directory-peer/pki/org/directory-peer.pem ]; then \
		bash fsc-infra/pki/bootstrap-directory-peer.sh; \
	fi

fsc-directory-up: fsc-directory-certs
	docker compose -f fsc-infra/docker-compose.yml up --build -d cfssl certportal postgres directory-migrations-controller directory-migrations-manager directory-migrations-txlog-api directory-controller directory-manager directory-inway directory-txlog-api directory-ui

fsc-edi-certs: fsc-up
	@if [ ! -f fsc-infra/orgs/edi-issuer/pki/org/edi-issuer.pem ]; then \
		bash fsc-infra/pki/bootstrap-edi-issuer.sh; \
	fi

fsc-edi-up: fsc-directory-up fsc-edi-certs
	docker compose -f fsc-infra/docker-compose.yml up --build -d cfssl certportal postgres directory-migrations-controller directory-migrations-manager directory-migrations-txlog-api directory-controller directory-manager directory-inway directory-txlog-api directory-ui edi-migrations-controller edi-migrations-manager edi-migrations-txlog-api edi-controller edi-manager edi-outway edi-txlog-api

fsc-bd-certs: fsc-up
	@if [ ! -f fsc-infra/orgs/belastingdienst-mock/pki/org/bd-mock.pem ]; then \
		bash fsc-infra/pki/bootstrap-bd-mock.sh; \
	fi

fsc-hv-certs: fsc-up
	@if [ ! -f fsc-infra/orgs/hypotheekverlener-mock/pki/org/hypotheekverlener.pem ]; then \
		bash fsc-infra/pki/bootstrap-hypotheekverlener.sh; \
	fi

fsc-bd-up: fsc-directory-up fsc-bd-certs
	docker compose -f fsc-infra/docker-compose.yml up --build -d cfssl certportal postgres directory-migrations-controller directory-migrations-manager directory-migrations-txlog-api directory-controller directory-manager directory-inway directory-txlog-api directory-ui bd-migrations-controller bd-migrations-manager bd-migrations-txlog-api bd-controller bd-manager bd-inway bd-txlog-api

fsc-pdp-cert:
	@if [ ! -f services/openftv-pdp/certs/pdp-service.pem ]; then \
		bash fsc-infra/pki/generate-pdp-cert.sh; \
	fi

fsc-all-up: fsc-directory-certs fsc-edi-certs fsc-bd-certs fsc-hv-certs fsc-pdp-cert
	docker compose -f fsc-infra/docker-compose.yml up --build -d

fsc-down:
	docker compose -f fsc-infra/docker-compose.yml down

fsc-test: fsc-up
	bash fsc-infra/test/request-org-cert.sh

fsc-clean:
	docker compose -f fsc-infra/docker-compose.yml down -v --rmi local
	rm -f fsc-infra/pki/ca/*.pem fsc-infra/pki/ca/*.csr
	rm -f fsc-infra/directory-peer/pki/org/*.pem
	rm -f fsc-infra/directory-peer/pki/internal/*.pem
	rm -f fsc-infra/directory-ui/pki/org/*.pem
	rm -f fsc-infra/orgs/edi-issuer/pki/org/*.pem
	rm -f fsc-infra/orgs/edi-issuer/pki/internal/*.pem
	rm -f fsc-infra/orgs/belastingdienst-mock/pki/org/*.pem
	rm -f fsc-infra/orgs/belastingdienst-mock/pki/internal/*.pem
	rm -f fsc-infra/orgs/hypotheekverlener-mock/pki/org/*.pem
	rm -f fsc-infra/orgs/hypotheekverlener-mock/pki/internal/*.pem
	rm -f services/openftv-pdp/certs/*.pem

# Contract-seed: register bri-service + publication + connection contract
# via mTLS to the FSC Manager/Controller APIs. Requires that fsc-all-up
# has been run and that directory-manager + bd-manager run with the
# --auto-sign-grants flags (see fsc-infra/docker-compose.yml). Runs in
# the pki-tools image inside fsc-infra_default so the script can reach
# managers via in-network hostnames.
fsc-seed-bri: fsc-local-env
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh

# Connection HV -> BD for bri (DvTP consumer with
# subject_id_type=pseudonym). The bri publication contract already
# exists via fsc-seed-bri; only the extra consumer connection is needed.
fsc-seed-bri-hv: fsc-local-env
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-connection-hv.sh

# Second bron behind the same provider peer: the BRP service. Same script,
# different service-name/endpoint/grant-link — see the header of
# seed-bri-contract.sh. EUDI-only for now (no HV connection), because the
# akte van overlijden is a wallet usecase.
fsc-seed-brp: fsc-local-env
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-e SERVICE_NAME=brp \
		-e SERVICE_ENDPOINT_URL=http://brp-sidecar:4011 \
		-e GRANT_LINK_PATH=/brp \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh

# Dedicated, non-public metadata service. The Outway path mirrors the FSC
# service reference, so onboarding needs no source-owned URL configuration.
fsc-seed-metadata: fsc-local-env
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-e SERVICE_NAME=gbo-attestation-metadata \
		-e SERVICE_ENDPOINT_URL=http://graphql-server:4000 \
		-e GRANT_LINK_PATH=/gbo-attestation-metadata \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-e SERVICE_NAME=brp-attestation-metadata \
		-e SERVICE_ENDPOINT_URL=http://brp-graphql-server:4001 \
		-e GRANT_LINK_PATH=/brp-attestation-metadata \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh
