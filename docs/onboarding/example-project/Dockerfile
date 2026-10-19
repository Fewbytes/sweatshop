# Worked example of a project image on the station base (see
# docs/onboarding/new-project.md). Build from the repository root after
# `just station-image`:
#   docker build -f docs/onboarding/example-project/Dockerfile -t myproject-station:dev .
# This one adds a Rust toolchain; replace that RUN block with your own.
ARG BASE_IMAGE=sweatshop-station:dev
FROM ${BASE_IMAGE}

# The base runs as the unprivileged agent user; install system packages as root.
USER root
ARG RUST_VERSION=1.90.0
ENV RUSTUP_HOME=/opt/rust/rustup CARGO_HOME=/opt/rust/cargo
ENV PATH=/opt/rust/cargo/bin:$PATH
RUN set -eux; \
    mkdir -p /opt/rust; \
    curl -fsSL https://sh.rustup.rs | sh -s -- -y --no-modify-path --profile minimal --default-toolchain "${RUST_VERSION}"; \
    chmod -R a+rX /opt/rust; \
    rustc --version; cargo --version

# Back to the base's user; the entrypoint and tini are inherited.
USER agent
