# syntax=docker/dockerfile:1

# Multi-stage build for nl-wallet's verification_server: the OpenID4VP
# verifier with which the ASI authorization server has the citizen disclose
# the PID from the NL Wallet (slice 7 of #336). Same nl-wallet checkout and
# build as eudi-issuance-server, so server and wallet app stay in lockstep.
# Sessions are kept in memory (storage.url memory://), so there is no
# database and no migrations binary.
#
# Build context: root of the nl-wallet repo. Example:
#   docker build -t gbo/asi-wallet-verifier:dev \
#     -f services/asi-wallet-verifier/Dockerfile "$NLWALLET_PATH"
#
# We build the binaries ourselves instead of reusing upstream's
# Dockerfiles, which are runtime-only (scratch + COPY of a CI-built
# binary). Build invocation matches upstream CI (deploy/gitlab/rust.yml):
#   cargo build --locked --target x86_64-unknown-linux-musl --release \
#     --package verification_server --bin verification_server
# Static musl build → scratch runtime, no dynamic libs needed.

# ─── Stage 1: build ────────────────────────────────────────────────────
# The release currently targets amd64. Keeping the target explicit makes a
# future native arm64 job possible without silently falling back to QEMU.
FROM rust:1.94-slim AS chef
ARG CARGO_CHEF_VERSION=0.1.77
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
      musl-tools \
      perl \
      make \
      pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN case "${TARGETARCH}" in \
      amd64) rust_target=x86_64-unknown-linux-musl ;; \
      arm64) rust_target=aarch64-unknown-linux-musl ;; \
      *) echo "Unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && rustup target add "${rust_target}" \
    && cargo install cargo-chef \
      --locked \
      --version "${CARGO_CHEF_VERSION}"

WORKDIR /src

# Generate a dependency-only recipe. Source-only changes leave this recipe
# unchanged, so BuildKit can restore the expensive `cargo chef cook` layer
# from the external GHA cache.
FROM chef AS planner
COPY wallet_core /src/wallet_core
COPY .cargo /src/.cargo

WORKDIR /src/wallet_core
RUN cargo chef prepare --recipe-path recipe.json

# Keep Cargo's registry and target directory in ordinary image layers rather
# than cache mounts: external BuildKit exporters do not persist cache mounts
# between ephemeral GitHub-hosted runners.
FROM chef AS builder
ARG TARGETARCH
COPY .cargo /src/.cargo
WORKDIR /src/wallet_core
COPY --from=planner /src/wallet_core/recipe.json recipe.json
RUN case "${TARGETARCH}" in \
      amd64) rust_target=x86_64-unknown-linux-musl ;; \
      arm64) rust_target=aarch64-unknown-linux-musl ;; \
      *) echo "Unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && cargo chef cook \
      --locked \
      --release \
      --target "${rust_target}" \
      --package verification_server \
      --bins \
      --recipe-path recipe.json

COPY wallet_core /src/wallet_core
RUN case "${TARGETARCH}" in \
      amd64) rust_target=x86_64-unknown-linux-musl ;; \
      arm64) rust_target=aarch64-unknown-linux-musl ;; \
      *) echo "Unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && cargo build \
      --locked \
      --release \
      --target "${rust_target}" \
      --package verification_server \
      --bins \
    && cp \
      "target/${rust_target}/release/verification_server" \
      /out-verification_server

# ─── Stage 2: runtime ──────────────────────────────────────────────────
FROM scratch
WORKDIR /
COPY --from=builder /out-verification_server /verification_server
# The configuration (verification_server.toml) is read from the working
# directory; compose mounts it next to the binary.
USER 1000
ENTRYPOINT []
CMD ["/verification_server"]
