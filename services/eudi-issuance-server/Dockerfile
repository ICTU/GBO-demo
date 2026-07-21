# Multi-stage build for nl-wallet's issuance_server + migrations.
#
# Build context: root of the nl-wallet repo. Example:
#   docker build -t gbo/eudi-issuance-server:dev \
#     -f services/eudi-issuance-server/Dockerfile "$NLWALLET_PATH"
#
# We build the binaries ourselves instead of reusing upstream's
# Dockerfiles, which are runtime-only (scratch + COPY of a CI-built
# binary). Build invocation matches upstream CI (deploy/gitlab/rust.yml):
#   cargo build --locked --target x86_64-unknown-linux-musl --release \
#     --package issuance_server --bin issuance_server
# Static musl build → scratch runtime, no dynamic libs needed.

# ─── Stage 1: build ────────────────────────────────────────────────────
# Buildx executes this stage for each target platform. On an amd64 runner,
# the arm64 variant runs through QEMU. TARGETARCH selects the matching Rust
# musl target so the image manifest and contained binary always agree.
FROM rust:1.94-slim AS builder
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
    && rustup target add "${rust_target}"

WORKDIR /src
COPY wallet_core /src/wallet_core
COPY .cargo /src/.cargo

WORKDIR /src/wallet_core
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/wallet_core/target \
    case "${TARGETARCH}" in \
      amd64) rust_target=x86_64-unknown-linux-musl ;; \
      arm64) rust_target=aarch64-unknown-linux-musl ;; \
    esac \
    && cargo build --locked \
      --target "${rust_target}" --release \
      --package issuance_server --bin issuance_server \
    && cp "target/${rust_target}/release/issuance_server" /out-issuance_server

RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/wallet_core/target \
    case "${TARGETARCH}" in \
      amd64) rust_target=x86_64-unknown-linux-musl ;; \
      arm64) rust_target=aarch64-unknown-linux-musl ;; \
    esac \
    && cargo build --locked \
      --target "${rust_target}" --release \
      --package issuance_server_migrations --bin issuance_server_migrations \
    && cp "target/${rust_target}/release/issuance_server_migrations" /out-issuance_server_migrations

# ─── Stage 2: runtime ──────────────────────────────────────────────────
FROM scratch
WORKDIR /
COPY --from=builder /out-issuance_server /issuance_server
COPY --from=builder /out-issuance_server_migrations /issuance_server_migrations
# The `metadata` reference in the TOML is resolved relative to the CWD
# (settings.rs:prefix_local_path), so config mounts land next to the
# binary in compose.
USER 1000
ENTRYPOINT []
CMD ["/issuance_server"]
