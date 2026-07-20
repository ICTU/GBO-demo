# Multi-stage Docker build voor NL-wallet issuance_server + migrations.
#
# Build context: de root van de nl-wallet-repo (zip-download of clone).
# Voorbeeld:
#   export NLWALLET_PATH=/Users/jeroendekok/projects/nl-wallet
#   docker build -t gbo/eudi-issuance-server:dev \
#     -f 05-demo/services/eudi-issuance-server/Dockerfile $NLWALLET_PATH
#
# Waarom in onze repo (i.p.v. upstream Dockerfile hergebruiken):
# nl-wallet's upstream Dockerfiles zijn runtime-only ("scratch + COPY
# pre-built binary") en verwachten dat CI de binary al bakt. Voor lokale
# reproduceerbaarheid bouwen we hier zelf de binary in stage 1.
#
# Musl static build volgt CI-invocation exact (deploy/gitlab/rust.yml):
#   cargo build --locked --target x86_64-unknown-linux-musl --release \
#     --no-default-features --features postgres
# → zero dynamic libs (rustls-tls, sqlx-postgres pure-Rust); geen
# openssl-dev / libpq nodig, alleen musl-toolchain.

# ─── Stage 1: bouw beide binaries statisch ─────────────────────────────
# --platform=linux/amd64 forceert build via QEMU op ARM-hosts (Mac Silicon).
# QEMU-emulated build is een eenmalige cold-cost; runtime-container blijft
# native amd64.
#
# Debian-slim (i.p.v. Alpine) sluit aan bij upstream CI (deploy/docker-images/
# ci/rust-root.sh gebruikt Debian) én levert `perl` + `make` mee — die zijn
# nodig voor de vendored openssl-sys-build die één van de transitieve deps
# meebrengt (sea-orm's connection-manager ondanks --no-default-features).
FROM --platform=linux/amd64 rust:1.92-slim AS builder
RUN apt-get update && apt-get install -y --no-install-recommends \
      musl-tools \
      perl \
      make \
      pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add x86_64-unknown-linux-musl

WORKDIR /src
# Workspace-manifests + source. `.cargo/`-config uit upstream wordt
# meegecopied als 'ie bestaat (bevat bv. rustflags-tuning voor musl).
COPY wallet_core /src/wallet_core
COPY .cargo /src/.cargo

WORKDIR /src/wallet_core
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/wallet_core/target \
    cargo build --locked \
      --target x86_64-unknown-linux-musl --release \
      --no-default-features --features postgres \
      --package issuance_server --bin issuance_server \
    && cp target/x86_64-unknown-linux-musl/release/issuance_server /out-issuance_server

RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/wallet_core/target \
    cargo build --locked \
      --target x86_64-unknown-linux-musl --release \
      --package issuance_server_migrations --bin issuance_server_migrations \
    && cp target/x86_64-unknown-linux-musl/release/issuance_server_migrations /out-issuance_server_migrations

# ─── Stage 2: runtime — scratch met alleen wat we nodig hebben ─────────
FROM scratch
WORKDIR /
COPY --from=builder /out-issuance_server /issuance_server
COPY --from=builder /out-issuance_server_migrations /issuance_server_migrations
# `metadata`-verwijzing in de TOML wordt CWD-relatief geresolved
# (settings.rs:prefix_local_path). Zorg dat metadata + auth-JSONs naast
# de binary staan wanneer de compose die mount.
USER 1000
ENTRYPOINT []
CMD ["/issuance_server"]
