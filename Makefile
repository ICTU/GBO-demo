.PHONY: up down logs clean certs fsc-ca fsc-up fsc-down fsc-test fsc-clean \
        fsc-seed-bri fsc-seed-bri-hv fsc-seed-brp fsc-pdp-cert eudi-images \
        demo demo-minimal demo-dvtp demo-eudi demo-full demo-down eudi-config

-include .env
-include fsc-infra/.env
export

# nl-wallet source for the eudi-issuance-server build. Pinned via git
# submodule (vendor/nl-wallet, v0.5.0). Server and wallet app move in
# lockstep: a v0.5.0 server rejects a v0.4.1 app and vice versa, because
# v0.5.0 made the `x509_san_dns:` client_id prefix mandatory. Override in
# .env if needed.
NLWALLET_PATH ?= $(PWD)/vendor/nl-wallet

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
EUDI_REQUIRED_VARS := EUDI_PUBLIC_URL EUDI_BRI_URL \
    EUDI_READER_KEY EUDI_READER_CERT \
    EUDI_ISSUER_KEY EUDI_ISSUER_CERT \
    EUDI_STATUS_KEY EUDI_STATUS_CERT

eudi-config:
	@command -v envsubst >/dev/null 2>&1 || { \
	  echo "ERROR: envsubst not found. Install with: brew install gettext"; \
	  exit 1; \
	}
	@set -a; [ -f .env ] && . ./.env; set +a; \
	missing=""; for v in $(EUDI_REQUIRED_VARS); do \
	  eval "val=\$$$$v"; \
	  [ -n "$$val" ] || missing="$$missing $$v"; \
	done; \
	if [ -n "$$missing" ]; then \
	  echo "ERROR: missing env-vars (see .env.example):$$missing"; \
	  exit 1; \
	fi; \
	if [ -z "$$EUDI_BRP_ISSUER_CERT" ] || [ -z "$$EUDI_BRP_READER_CERT" ]; then \
	  echo "WARNING: EUDI_BRP_{ISSUER,READER}_{KEY,CERT} not set — the akte van"; \
	  echo "         overlijden falls back to the Belastingdienst issuer/reader"; \
	  echo "         certificates. The wallet will then show 'Belastingdienst' as"; \
	  echo "         the issuer and 'Uitgifte inkomensverklaring' as the reason for"; \
	  echo "         sharing the BSN, which is wrong for a BRP akte. Mint a BRP pair"; \
	  echo "         from akte_van_overlijden_{issuer,reader}_auth.json to fix it."; \
	fi; \
	export EUDI_BRP_ISSUER_KEY="$${EUDI_BRP_ISSUER_KEY:-$$EUDI_ISSUER_KEY}"; \
	export EUDI_BRP_ISSUER_CERT="$${EUDI_BRP_ISSUER_CERT:-$$EUDI_ISSUER_CERT}"; \
	export EUDI_BRP_READER_KEY="$${EUDI_BRP_READER_KEY:-$$EUDI_READER_KEY}"; \
	export EUDI_BRP_READER_CERT="$${EUDI_BRP_READER_CERT:-$$EUDI_READER_CERT}"; \
	for f in $(EUDI_CONFIG_FILES); do \
	  echo "-> Rendering $(EUDI_CONFIG_DIR)/$$f from $$f.example"; \
	  envsubst < $(EUDI_CONFIG_DIR)/$$f.example > $(EUDI_CONFIG_DIR)/$$f; \
	done

# eudi-issuance-server has no published image — built from the local
# nl-wallet checkout ($NLWALLET_PATH). The build is expensive, so an
# existing image is reused — but only while it was built from the sources
# now on disk. The nl-wallet revision is stamped on the image as a label
# and compared here, so bumping the submodule (or editing an overridden
# checkout) rebuilds instead of silently running the previous pin's binary
# against this pin's config. Server and wallet app move in lockstep; a
# stale binary here is a broken flow, not a slightly older one.
eudi-images:
	@if [ ! -f "$$NLWALLET_PATH/wallet_core/Cargo.toml" ]; then \
	  echo "ERROR: nl-wallet sources not found at $$NLWALLET_PATH"; \
	  echo "       Run: git submodule update --init vendor/nl-wallet"; \
	  exit 1; \
	fi
	@rev="$$(git -C "$$NLWALLET_PATH" describe --tags --always --dirty 2>/dev/null || echo unknown)"; \
	built="$$(docker image inspect gbo/eudi-issuance-server:dev \
	    --format '{{index .Config.Labels "gbo.nlwallet-rev"}}' 2>/dev/null || true)"; \
	if [ -n "$$built" ] && [ "$$built" = "$$rev" ]; then \
	  echo "-> gbo/eudi-issuance-server:dev is current (nl-wallet $$rev)"; \
	else \
	  if [ -n "$$built" ]; then \
	    echo "-> Rebuilding gbo/eudi-issuance-server:dev — image holds nl-wallet $$built, checkout is $$rev"; \
	  else \
	    echo "-> Building gbo/eudi-issuance-server:dev from $$NLWALLET_PATH (nl-wallet $$rev)"; \
	  fi; \
	  docker build -t gbo/eudi-issuance-server:dev \
	    --label gbo.nlwallet-rev="$$rev" \
	    -f services/eudi-issuance-server/Dockerfile "$$NLWALLET_PATH"; \
	fi

demo-eudi: certs fsc-all-up fsc-seed-bri fsc-seed-brp eudi-config eudi-images
	@echo "-> EUDI stack: base + eudi branch + fsc-infra"
	docker compose --profile eudi up --build -d
	@echo ""
	@echo "  Dev-portal:      http://localhost:9003  |  http://$$(hostname -I | awk '{print $$1}'):9003"
	@echo "  EUDI-adapter:    http://localhost:9409  |  http://$$(hostname -I | awk '{print $$1}'):9409"
	@echo "  Jaeger:          http://localhost:9686  |  http://$$(hostname -I | awk '{print $$1}'):9686"
	@echo ""
	@echo "  Manual step: grant-links '/bri' and '/brp' in EDI-Controller-UI"
	@echo "  (see README.md section 'EUDI flow over real FSC' step 3)"

demo-full: certs fsc-all-up fsc-seed-bri fsc-seed-brp fsc-seed-bri-hv eudi-config eudi-images
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

fsc-ca:
	@if [ ! -f fsc-infra/pki/ca/root.pem ]; then bash fsc-infra/pki/generate-root-ca.sh; fi

fsc-up: fsc-ca
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
fsc-seed-bri:
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
fsc-seed-bri-hv:
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
fsc-seed-brp:
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
