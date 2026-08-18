.PHONY: up down logs clean certs demo-manager manager-seed fsc-local-env fsc-ca fsc-up fsc-all-up fsc-brp-certs fsc-databases fsc-down fsc-test fsc-clean \
        fsc-seed-bri fsc-seed-bri-hv fsc-seed-brp fsc-seed-rvig-source fsc-seed-metadata fsc-pdp-cert pdp-up \
        eudi-images source-metadata-up \
        provision-development-certificates reconcile-sources onboard-demo-sources onboarding-directories demo demo-minimal demo-dvtp demo-eudi \
        demo-full demo-down eudi-config

-include .env
-include fsc-infra/.env
export

# nl-wallet source for the eudi-issuance-server build. Pinned via git
# submodule (vendor/nl-wallet, v0.5.0). Server and wallet app move in
# lockstep: a v0.5.0 server rejects a v0.4.1 app and vice versa, because
# v0.5.0 made the `x509_san_dns:` client_id prefix mandatory. Override in
# .env if needed.
NLWALLET_PATH ?= $(PWD)/vendor/nl-wallet

# Local filesystem onboarding. Development certificates are provisioned only
# by the explicit provision-development-certificates target.
DEVELOPMENT_SOURCE_OIN ?= 99999999900000000200
DEVELOPMENT_BRP_SOURCE_OIN ?= $(DEVELOPMENT_SOURCE_OIN)
DEVELOPMENT_SOURCE_NAME ?= Belastingdienst
DEVELOPMENT_BRP_SOURCE_NAME ?= RvIG
DEVELOPMENT_SOURCE_LOGO ?= assets/issuer-logos/belastingdienst.svg
DEVELOPMENT_BRP_SOURCE_LOGO ?= assets/issuer-logos/rvig.svg
DEVELOPMENT_UNSECURED_SOURCE_OIN ?= 99999999900000000900
DEVELOPMENT_UNSECURED_SOURCE_NAME ?= Demo unsecured source
DEVELOPMENT_UNSECURED_SOURCE_LOGO ?= assets/issuer-logos/belastingdienst.svg
ONBOARDING_OUTWAY_URL ?= http://localhost:$(or $(FSC_PORT_EDI_OUTWAY),8087)
ONBOARDING_STATE_DIR ?= $(PWD)/.local/onboarding
ONBOARDING_SECRETS_DIR ?= $(PWD)/.local/secrets
ONBOARDING_TYPE_METADATA_URL ?= $(or $(EUDI_BRI_URL),http://localhost:$(or $(GBO_PORT_EUDI_ADAPTER),9409))
ONBOARDING_STORAGE_BACKEND ?= filesystem
ONBOARDING_CERTIFICATE_STORE ?= filesystem

# Docker network of the fsc-infra instance this checkout uses. Equals
# <FSC_PROJECT_NAME>_default; override in fsc-infra/.env to run a
# per-worktree fsc-infra side by side with another checkout's.
FSC_PROJECT_NAME ?= fsc-infra
FSC_INFRA_NETWORK ?= $(FSC_PROJECT_NAME)_default
FSC_COMPOSE = docker compose -p $(FSC_PROJECT_NAME) -f fsc-infra/docker-compose.yml

# Published host ports, with the same defaults compose applies. The demo
# banners must read these too: a checkout that shifts its ports in .env
# to run beside another worktree was handed URLs that do not resolve.
PORT_DEV_PORTAL := $(or $(GBO_PORT_DEVELOPER_PORTAL),9003)
PORT_TOESTEMMINGSPORTAAL := $(or $(GBO_PORT_TOESTEMMINGSPORTAAL),9002)
PORT_DV_MOCK := $(or $(GBO_PORT_DV_MOCK),9001)
PORT_JAEGER := $(or $(GBO_PORT_JAEGER),9686)
PORT_PDP := $(or $(GBO_PORT_OPA),9181)
PORT_EUDI_ADAPTER := $(or $(GBO_PORT_EUDI_ADAPTER),9409)
PORT_DVTP_ONBOARDING_REGISTER := $(or $(GBO_PORT_DVTP_ONBOARDING_REGISTER),9415)

# banner <label> <port> [scheme] — one "  Label:  <local>  |  <lan>" line.
# hostname -I lists every address; the first is the LAN one.
banner = @printf "  %-21s %s://localhost:%s  |  %s://%s:%s\n" "$(1)" "$(or $(3),http)" "$(2)" "$(or $(3),http)" "$$(hostname -I | awk '{print $$1}')" "$(2)"

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
#   make demo-manager  → base + OpenFTV Manager owning policy distribution
#   make demo-down     → everything down (main + fsc-infra)

demo: demo-dvtp

demo-minimal: certs
	@echo "-> Base stack (no profile)"
	docker compose up --build -d
	@echo ""
	$(call banner,Dev-portal:,$(PORT_DEV_PORTAL))
	$(call banner,Jaeger:,$(PORT_JAEGER))
	@echo "  OpenFTV PDP:          https://localhost:$(PORT_PDP)/authzen/v1/evaluation (POST)"

# The management plane: the Manager owns the policies and ships them to the
# PDP as a bundle, instead of the PDP loading ./policies from disk. Opt-in,
# because it trades the edit-and-save hot-reload loop for a deliberate
# deploy step — which is the point, but not what you want while writing Rego.
demo-manager: certs
	@test -n "$(KEYCLOAK_ADMIN_PASSWORD)" || (echo "KEYCLOAK_ADMIN_PASSWORD is required" >&2; exit 1)
	@test -n "$(FTV_MANAGER_AUDITOR_PASSWORD)" || (echo "FTV_MANAGER_AUDITOR_PASSWORD is required" >&2; exit 1)
	@test -n "$(FTV_MANAGER_DEPLOY_PASSWORD)" || (echo "FTV_MANAGER_DEPLOY_PASSWORD is required" >&2; exit 1)
	@test -n "$(FTV_POSTGRES_PASSWORD)" || (echo "FTV_POSTGRES_PASSWORD is required" >&2; exit 1)
	@echo "-> Base stack + OpenFTV Manager (PAP/PIP, bundle distribution)"
	GBO_BUNDLE_MANAGER=http://openftv-manager:9443/v1/bundle/gbo-pdp \
	  GBO_ADL_TYPE=postgres \
	  GBO_ADL_PG_URL=postgres://ftv:$${FTV_POSTGRES_PASSWORD}@postgres-ftv:5432/ftv_adl?sslmode=disable \
	  GBO_ADL_MIGRATE_SOURCE='*EMBED*' GBO_ADL_MIGRATE_AUTO=true \
	  docker compose --profile manager up --build -d
	@echo "-> Waiting for the Manager to accept policies..."
	@for i in $$(seq 1 30); do \
	  curl -fsS -m 2 http://localhost:$${GBO_PORT_FTV_MANAGER_HEALTH:-9282}/healthz >/dev/null 2>&1 && break; \
	  sleep 2; \
	done
	./scripts/seed-openftv-manager.py --url http://localhost:$${GBO_PORT_FTV_MANAGER:-9280}
	@echo "-> Restarting the PDP so it pulls the freshly seeded bundle"
	docker compose --profile manager restart openftv-pdp
	@echo ""
	@echo "  Manager API:   http://localhost:$${GBO_PORT_FTV_MANAGER:-9280}/v1/policies"
	@echo "  Bundle (PDP):  http://localhost:$${GBO_PORT_FTV_MANAGER_INTERNAL:-9281}/v1/bundle/gbo-pdp"
	@echo "  Re-seed after editing policies/:  make manager-seed"

# Push the current policies/ into the Manager and redeploy them. Policies
# that no longer exist in git are retired (untagged) rather than deleted —
# DELETE is broken upstream, see ICTU-37.
manager-seed:
	@test -n "$(FTV_MANAGER_DEPLOY_PASSWORD)" || (echo "FTV_MANAGER_DEPLOY_PASSWORD is required" >&2; exit 1)
	./scripts/seed-openftv-manager.py --url http://localhost:$${GBO_PORT_FTV_MANAGER:-9280}
	docker compose --profile manager restart openftv-pdp

demo-dvtp: certs fsc-all-up fsc-seed-bri fsc-seed-bri-hv
	@echo "-> DvTP stack: base + dienstverlener + toestemmingsportaal (via real FSC)"
	docker compose --profile dvtp up --build -d
	@echo ""
	$(call banner,Dev-portal:,$(PORT_DEV_PORTAL))
	$(call banner,Toestemmingsportaal:,$(PORT_TOESTEMMINGSPORTAAL))
	$(call banner,Dienstverlener:,$(PORT_DV_MOCK))
	@printf "  %-21s http://localhost:%s\n" "DvTP toelatingsregister:" "$(PORT_DVTP_ONBOARDING_REGISTER)"
	$(call banner,Jaeger:,$(PORT_JAEGER))

EUDI_CONFIG_DIR := services/eudi-issuance-server/config
EUDI_REQUIRED_VARS := EUDI_PUBLIC_URL EUDI_BRI_URL

eudi-config:
	@set -eu; set -a; [ -f .env ] && . ./.env; set +a; \
	missing=""; for v in $(EUDI_REQUIRED_VARS); do \
	  eval "val=\$$$$v"; \
	  [ -n "$$val" ] || missing="$$missing $$v"; \
	done; \
	if [ -n "$$missing" ]; then \
	  echo "ERROR: missing env-vars (see .env.example):$$missing"; \
	  exit 1; \
	fi; \
	docker compose --profile onboarding run --build --rm source-reconciler \
	  ./eudi-adapter generate-issuance-config \
	  --activations-dir /var/lib/gbo/candidates \
	  --active-dir /var/lib/gbo/active \
	  --sources-dir /config/sources \
	  --status-dir /var/lib/gbo/status \
	  --template /generated/issuance_server.toml.example \
	  --adapter-base-url "$$EUDI_BRI_URL" \
	  --output /generated/issuance_server.toml \
	  --offers-output /generated/eudi-offers.json; \
	cp "$(PWD)/$(EUDI_CONFIG_DIR)/eudi-offers.json" "$(PWD)/landing-page/public/eudi-offers.json"; \
	cp "$(PWD)/$(EUDI_CONFIG_DIR)/eudi-offers.json" "$(PWD)/developer-portal/public/eudi-offers.json"; \
	echo "-> Generated issuance products and frontend offer catalog from every active source"

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

source-metadata-up:
	EUDI_PUBLIC_URL="$${EUDI_PUBLIC_URL:-http://localhost:8001}" \
	EUDI_BRI_URL="$${EUDI_BRI_URL:-http://localhost:9409}" \
	EUDI_POSTGRES_PASSWORD="$${EUDI_POSTGRES_PASSWORD:-local-not-used}" \
		docker compose up --build --force-recreate -d openftv-pdp additional-claims-service graphql-server brp-graphql-server unsecured-graphql-server bron-sidecar brp-sidecar

onboarding-directories:
	@mkdir -p "$(ONBOARDING_STATE_DIR)/type-metadata" "$(ONBOARDING_STATE_DIR)/candidates" "$(ONBOARDING_STATE_DIR)/active" "$(ONBOARDING_STATE_DIR)/status"

provision-development-certificates:
	@test -n "$(SOURCE_OIN)" || { echo "ERROR: SOURCE_OIN=<20-digit OIN> is required"; exit 1; }
	@cd services/eudi-adapter && go run . provision-development-certificates \
		--source-oin "$(SOURCE_OIN)" \
		--source-name "$(SOURCE_NAME)" \
		--certificate-set "$(SOURCE_CERTIFICATE_SET)" \
		--source-logo "$(if $(SOURCE_LOGO),$(abspath $(SOURCE_LOGO)),)" \
		--reader-public-url "$${EUDI_PUBLIC_URL:-}" \
		--secrets-dir "$(ONBOARDING_SECRETS_DIR)"

reconcile-sources: onboarding-directories
	docker compose --profile onboarding run --build --rm source-reconciler

# Complete, idempotent local onboarding for both demo sources. The sequence is
# explicit so a clean checkout cannot reach eudi-config before metadata has
# been published, transported through FSC, verified and activated.
onboard-demo-sources: certs
	$(MAKE) fsc-all-up
	$(MAKE) source-metadata-up
	$(MAKE) fsc-seed-bri
	$(MAKE) fsc-seed-rvig-source
	$(MAKE) fsc-seed-metadata
	$(MAKE) provision-development-certificates SOURCE_OIN=$(DEVELOPMENT_SOURCE_OIN) SOURCE_CERTIFICATE_SET=belastingdienst SOURCE_NAME="$(DEVELOPMENT_SOURCE_NAME)" SOURCE_LOGO="$(DEVELOPMENT_SOURCE_LOGO)"
	$(MAKE) provision-development-certificates SOURCE_OIN=$(DEVELOPMENT_BRP_SOURCE_OIN) SOURCE_CERTIFICATE_SET=rvig SOURCE_NAME="$(DEVELOPMENT_BRP_SOURCE_NAME)" SOURCE_LOGO="$(DEVELOPMENT_BRP_SOURCE_LOGO)"
	$(MAKE) provision-development-certificates SOURCE_OIN=$(DEVELOPMENT_UNSECURED_SOURCE_OIN) SOURCE_CERTIFICATE_SET=demo-unsecured SOURCE_NAME="$(DEVELOPMENT_UNSECURED_SOURCE_NAME)" SOURCE_LOGO="$(DEVELOPMENT_UNSECURED_SOURCE_LOGO)"
	$(MAKE) reconcile-sources

demo-eudi: onboard-demo-sources eudi-config eudi-images
	@echo "-> EUDI stack: base + eudi branch + fsc-infra"
	docker compose --profile eudi up --build -d
	@echo ""
	$(call banner,Dev-portal:,$(PORT_DEV_PORTAL))
	$(call banner,EUDI-adapter:,$(PORT_EUDI_ADAPTER))
	$(call banner,Jaeger:,$(PORT_JAEGER))

demo-full: onboard-demo-sources fsc-seed-bri-hv eudi-config eudi-images
	@echo "-> Full stack: everything on"
	docker compose --profile full up --build -d

demo-down:
	docker compose --profile full --profile manager down
	$(FSC_COMPOSE) down

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
	$(FSC_COMPOSE) up --build -d cfssl certportal

fsc-directory-certs: fsc-up
	@if [ ! -f fsc-infra/directory-peer/pki/org/directory-peer.pem ]; then \
		bash fsc-infra/pki/bootstrap-directory-peer.sh; \
	fi

fsc-directory-up: fsc-directory-certs
	$(FSC_COMPOSE) up --build -d cfssl certportal postgres directory-migrations-controller directory-migrations-manager directory-migrations-txlog-api directory-controller directory-manager directory-inway directory-txlog-api directory-ui

fsc-edi-certs: fsc-up
	@if [ ! -f fsc-infra/orgs/edi-issuer/pki/org/edi-issuer.pem ]; then \
		bash fsc-infra/pki/bootstrap-edi-issuer.sh; \
	fi

fsc-edi-up: fsc-directory-up fsc-edi-certs
	$(FSC_COMPOSE) up --build -d cfssl certportal postgres directory-migrations-controller directory-migrations-manager directory-migrations-txlog-api directory-controller directory-manager directory-inway directory-txlog-api directory-ui edi-migrations-controller edi-migrations-manager edi-migrations-txlog-api edi-controller edi-manager edi-outway edi-txlog-api

fsc-bd-certs: fsc-up
	@if [ ! -f fsc-infra/orgs/belastingdienst-mock/pki/org/bd-mock.pem ]; then \
		bash fsc-infra/pki/bootstrap-bd-mock.sh; \
	fi

fsc-brp-certs: fsc-up
	@if [ ! -f fsc-infra/orgs/brp-mock/pki/org/brp-mock.pem ]; then \
		bash fsc-infra/pki/bootstrap-brp-mock.sh; \
	fi

fsc-hv-certs: fsc-up
	@if [ ! -f fsc-infra/orgs/hypotheekverlener-mock/pki/org/hypotheekverlener.pem ]; then \
		bash fsc-infra/pki/bootstrap-hypotheekverlener.sh; \
	fi

fsc-bd-up: fsc-directory-up fsc-bd-certs fsc-pdp-cert
	$(MAKE) pdp-up
	$(FSC_COMPOSE) up --build -d cfssl certportal postgres directory-migrations-controller directory-migrations-manager directory-migrations-txlog-api directory-controller directory-manager directory-inway directory-txlog-api directory-ui bd-migrations-controller bd-migrations-manager bd-migrations-txlog-api bd-controller bd-manager bd-inway bd-txlog-api

fsc-pdp-cert:
	@if [ ! -f services/openftv-pdp/certs/pdp-service.pem ]; then \
		bash fsc-infra/pki/generate-pdp-cert.sh; \
	fi

# The source Managers decide on incoming contracts through the OpenFTV PDP
# (--authzen-auto-sign-pdp-address), so the PDP has to answer before the
# first contract is submitted — otherwise every fsc-seed-* leaves its
# connection contract pending and the seed times out after 30s with no
# useful error. The PDP lives in the main compose project, which normally
# starts *after* fsc-infra; this pulls it forward. --wait only covers the
# process healthcheck, so the explicit AuthZen probe also waits for the native
# PIP pull to make the seeded Hypotheek-BV admission effective.
pdp-up: certs fsc-pdp-cert
	docker compose up --build -d --wait --force-recreate openftv-pdp
	./scripts/wait-openftv-admission.sh

fsc-all-up: fsc-directory-certs fsc-edi-certs fsc-bd-certs fsc-brp-certs fsc-hv-certs fsc-pdp-cert
	$(MAKE) fsc-databases
	$(MAKE) pdp-up
	$(FSC_COMPOSE) up --build -d

# Also creates databases added after an existing local Postgres volume was
# initialised; the docker-entrypoint init script only runs for a new volume.
fsc-databases: fsc-local-env
	$(FSC_COMPOSE) up -d --wait postgres
	@container_id="$$($(FSC_COMPOSE) ps -q postgres)"; \
	ready=false; \
	for attempt in $$(seq 1 60); do \
		if docker logs "$$container_id" 2>&1 | grep -Eq \
			'PostgreSQL init process complete; ready for start up.|Skipping initialization'; then \
			if $(FSC_COMPOSE) exec -T postgres \
				psql -U postgres -d postgres -tAc 'SELECT 1' >/dev/null 2>&1; then \
				ready=true; \
				break; \
			fi; \
		fi; \
		sleep 1; \
	done; \
	if [ "$$ready" != "true" ]; then \
		echo "ERROR: FSC Postgres did not finish initialization" >&2; \
		exit 1; \
	fi
	@for database in fsc_brp_controller fsc_brp_manager fsc_brp_txlog; do \
		exists=$$($(FSC_COMPOSE) exec -T postgres \
			psql -U postgres -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$$database'"); \
		if [ "$$exists" != "1" ]; then \
			echo "-> Creating missing FSC database $$database"; \
			$(FSC_COMPOSE) exec -T postgres createdb -U postgres "$$database"; \
		fi; \
	done

fsc-down:
	$(FSC_COMPOSE) down

fsc-test: fsc-up
	bash fsc-infra/test/request-org-cert.sh

fsc-clean:
	$(FSC_COMPOSE) down -v --rmi local
	rm -f fsc-infra/pki/ca/*.pem fsc-infra/pki/ca/*.csr
	rm -f fsc-infra/directory-peer/pki/org/*.pem
	rm -f fsc-infra/directory-peer/pki/internal/*.pem
	rm -f fsc-infra/directory-ui/pki/org/*.pem
	rm -f fsc-infra/orgs/edi-issuer/pki/org/*.pem
	rm -f fsc-infra/orgs/edi-issuer/pki/internal/*.pem
	rm -f fsc-infra/orgs/belastingdienst-mock/pki/org/*.pem
	rm -f fsc-infra/orgs/belastingdienst-mock/pki/internal/*.pem
	rm -f fsc-infra/orgs/brp-mock/pki/org/*.pem
	rm -f fsc-infra/orgs/brp-mock/pki/internal/*.pem
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

# BRP/RvIG is a separate local FSC provider peer (OIN ...0400), with its own
# Manager, Controller, Inway and contracts.
fsc-seed-brp: fsc-local-env
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-e SERVICE_NAME=brp \
		-e SERVICE_ENDPOINT_URL=http://brp-sidecar:4011 \
		-e SERVICE_INWAY_ADDRESS=https://brp-inway:443 \
		-e PROVIDER_PEER_ID=99999999900000000400 \
		-e PROVIDER_CONTROLLER_URL=https://brp-controller:9444 \
		-e PROVIDER_MANAGER_URL=https://brp-manager:9443 \
		-e PROVIDER_INTERNAL_DIR=/work/orgs/brp-mock/pki/internal \
		-e CREATE_GRANT_LINK=false \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh

# The onboarding proof publishes the logical RvIG source through the same FSC
# participant as BD. It remains a distinct service, metadata document and
# certificate set; FSC only shares the provider organization and Inway.
fsc-seed-rvig-source: fsc-local-env
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-e SERVICE_NAME=brp \
		-e SERVICE_ENDPOINT_URL=http://brp-sidecar:4011 \
		-e CREATE_GRANT_LINK=false \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh

# One FSC participant publishes two logical metadata services. The manually
# configured service reference selects which source document is fetched.
fsc-seed-metadata: fsc-local-env
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-e SERVICE_NAME=gbo-metadata-bd \
		-e SERVICE_ENDPOINT_URL=http://graphql-server:4000 \
		-e CREATE_GRANT_LINK=false \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh
	docker run --rm \
		--network $(FSC_INFRA_NETWORK) \
		--env-file fsc-infra/.env \
		-e SERVICE_NAME=gbo-metadata-rvig \
		-e SERVICE_ENDPOINT_URL=http://brp-graphql-server:4001 \
		-e CREATE_GRANT_LINK=false \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh
