# Multi-stage build for nl-wallet's demo_issuer — the web server that
# renders the HTML with QR codes for the wallet app to scan. QR links
# contain universal-links plus the public issuance-server URL, where the
# wallet then starts the OpenID4VCI flow.
#
# Build context: nl-wallet repo root (same as the issuance-server Dockerfile).
#
# The demo_issuer provides a standalone, wallet-testable entry point:
# without this service there is no QR code to scan. Assets (SVGs/icons) are
# picked up in stage 2.

FROM --platform=linux/amd64 rust:1.94-slim AS builder
RUN apt-get update && apt-get install -y --no-install-recommends \
      musl-tools perl make pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add x86_64-unknown-linux-musl

WORKDIR /src
COPY wallet_core /src/wallet_core
COPY .cargo /src/.cargo

WORKDIR /src/wallet_core
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/wallet_core/target \
    cargo build --locked \
      --target x86_64-unknown-linux-musl --release \
      --package demo_issuer --bin demo_issuer \
    && cp target/x86_64-unknown-linux-musl/release/demo_issuer /out-demo_issuer

# demo_utils/lib.rs expects assets/nl-wallet-web.iife.js in CWD. It is
# built from wallet_web (Vite lib-build). Separate node stage.
FROM --platform=linux/amd64 node:20-alpine AS webbuilder
WORKDIR /wallet_web
COPY wallet_web /wallet_web
# vite.config.ts requires VITE_HELP_BASE_URL — pass it as a build arg;
# the default in demo_issuer.json is overridden at runtime anyway.
ENV VITE_HELP_BASE_URL=https://help.example.com/
RUN npm ci --no-audit --no-fund && npm run build

FROM --platform=linux/amd64 alpine:3.19 AS assets
COPY wallet_core/demo/demo_issuer/assets /assets-issuer
COPY wallet_core/demo/demo_utils/assets /assets-utils

FROM scratch
WORKDIR /
COPY --from=builder /out-demo_issuer /demo_issuer
COPY --from=assets /assets-issuer /assets/
COPY --from=assets /assets-utils /assets/
COPY --from=webbuilder /wallet_web/dist/nl-wallet-web.iife.js /assets/nl-wallet-web.iife.js
USER 0
ENTRYPOINT ["/demo_issuer"]
