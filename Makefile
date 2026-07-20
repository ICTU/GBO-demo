.PHONY: up down logs clean certs fsc-ca fsc-up fsc-down fsc-test fsc-clean fsc-seed-bri \
        demo demo-minimal demo-dvtp demo-eudi demo-full demo-down eudi-config

up: certs
	docker compose up --build -d

down:
	docker compose down

# ── Demo bootstrap targets ───────────────────────────────────────────────
# Each target brings up one of the compose-profile combinations. Certs
# are auto-generated on first run (idempotent); subsequent runs skip.
#
#   make demo          → default DvTP flow (over real FSC via Hypotheekverlener-mock)
#   make demo-minimal  → base only: adapter/pdp/opa/graphql + observability
#   make demo-dvtp     → alias for 'make demo'
#   make demo-eudi     → EUDI flow over real FSC (auto init + seed-bri)
#   make demo-full     → everything on (DvTP + EUDI + fsc-infra)
#   make demo-down     → everything down (main + fsc-infra)

demo: demo-dvtp

demo-minimal: certs
	@echo "-> Base stack (no profile): 13 services"
	docker compose up --build -d
	@echo ""
	@echo "  Dev-portal:    http://localhost:9003"
	@echo "  Jaeger:        http://localhost:9686"
	@echo "  pdp-service:   http://localhost:9408/evaluation (POST)"

demo-dvtp: certs
	@echo "-> DvTP stack: base + dienstverlener + toestemmingsportaal (via real FSC)"
	docker compose --profile dvtp up --build -d
	@echo ""
	@echo "  Dev-portal:          http://localhost:9003"
	@echo "  Toestemmingsportaal: http://localhost:9002"
	@echo "  Dienstverlener:      http://localhost:9001"
	@echo "  Jaeger:              http://localhost:9686"

EUDI_CONFIG_DIR := services/eudi-issuance-server/config
EUDI_CONFIG_FILES := issuance_server.toml inkomensverklaring_metadata.json issuer_auth.json reader_auth.json
EUDI_CONFIG_TARGETS := $(addprefix $(EUDI_CONFIG_DIR)/,$(EUDI_CONFIG_FILES))

eudi-config: $(EUDI_CONFIG_TARGETS)

$(EUDI_CONFIG_DIR)/%.toml $(EUDI_CONFIG_DIR)/%.json:
	@if [ -z "$$NLWALLET_PATH" ]; then \
	  echo "ERROR: NLWALLET_PATH not set. Point it at your nl-wallet checkout."; \
	  exit 1; \
	fi
	@src="$$NLWALLET_PATH/target/is-config/$(@F)"; \
	if [ ! -f "$$src" ]; then \
	  echo "ERROR: $$src not found."; \
	  echo "       Generate it in your nl-wallet checkout first (see nl-wallet docs)."; \
	  exit 1; \
	fi; \
	echo "-> Bootstrapping $(@F) from \$$NLWALLET_PATH (git-ignored locally — carries keys/hostnames tied to your certs)"; \
	cp "$$src" "$@"

demo-eudi: certs fsc-all-up fsc-seed-bri eudi-config
	@echo "-> EUDI stack: base + eudi branch + fsc-infra"
	docker compose --profile eudi up --build -d
	@echo ""
	@echo "  Dev-portal:      http://localhost:9003"
	@echo "  EUDI-adapter:    http://localhost:9409"
	@echo "  Jaeger:          http://localhost:9686"
	@echo ""
	@echo "  Manual step: grant-link '/bri' in EDI-Controller-UI"
	@echo "  (see README.md section 'EUDI flow over real FSC' step 3)"

demo-full: certs fsc-all-up fsc-seed-bri eudi-config
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

fsc-all-up: fsc-edi-certs fsc-bd-certs fsc-hv-certs
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

# Contract-seed: register bri-service + publication + connection contract
# via mTLS to the FSC Manager/Controller APIs. Requires that fsc-all-up
# has been run and that directory-manager + bd-manager run with the
# --auto-sign-grants flags (see fsc-infra/docker-compose.yml). Runs in
# the pki-tools image inside fsc-infra_default so the script can reach
# managers via in-network hostnames.
fsc-seed-bri:
	docker run --rm \
		--network fsc-infra_default \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-contract.sh

# Connection HV -> BD for bri (DvTP consumer with
# subject_id_type=pseudonym). The bri publication contract already
# exists via fsc-seed-bri; only the extra consumer connection is needed.
fsc-seed-bri-hv:
	docker run --rm \
		--network fsc-infra_default \
		-v $(PWD)/fsc-infra:/work:ro \
		-w /work \
		gbo-demo/pki-tools:local \
		bash scripts/seed-bri-connection-hv.sh
