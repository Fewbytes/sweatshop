# sweatshop-station: the Maker station's container and the line's check
# runner image, so an agent's `just ci` and the witness's `just ci` see one
# toolchain. Build from the repository root: `just station-image`.

# Keep in step with the `go` directive in line/go.mod (and agentsh/go.mod).
ARG GO_VERSION=1.24.9

FROM golang:${GO_VERSION}-bookworm AS builder
WORKDIR /build/line
COPY line/go.mod line/go.sum ./
RUN go mod download
COPY line/ ./
RUN CGO_ENABLED=0 go build -o /out/sweatshop ./cmd/sweatshop

# bookworm ships git 2.39 and bookworm-backports carries no git at all, so
# build the witness's floor (2.45.1) or newer from the kernel.org release.
FROM debian:bookworm-slim AS git-builder
ARG GIT_VERSION=2.51.0
ARG GIT_SHA256=60a7c2251cc2e588d5cd87bae567260617c6de0c22dca9cdbfc4c7d2b8990b62
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl xz-utils make gcc libc6-dev \
        libcurl4-openssl-dev libexpat1-dev zlib1g-dev libssl-dev; \
    curl -fsSLo /tmp/git.tar.xz "https://www.kernel.org/pub/software/scm/git/git-${GIT_VERSION}.tar.xz"; \
    echo "${GIT_SHA256}  /tmp/git.tar.xz" | sha256sum -c -; \
    mkdir /tmp/git && tar -xJf /tmp/git.tar.xz -C /tmp/git --strip-components=1; \
    make -C /tmp/git -j"$(nproc)" prefix=/opt/git NO_TCLTK=1 NO_GETTEXT=1 NO_PERL=1 NO_PYTHON=1 install

FROM debian:bookworm-slim
ARG UV_VERSION=0.12.23
ARG JUST_VERSION=1.58.0
ARG GIT_FLOOR=2.45.1

COPY --from=git-builder /opt/git /opt/git
ENV PATH=/opt/git/bin:$PATH
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl bash ripgrep fd-find python3 nodejs npm \
        build-essential pkg-config xz-utils libcurl4 libexpat1 zlib1g openssh-client less; \
    rm -rf /var/lib/apt/lists/*; \
    have="$(git --version | awk '{print $3}')"; \
    if [ "$(printf '%s\n%s\n' "$GIT_FLOOR" "$have" | sort -V | head -n1)" != "$GIT_FLOOR" ]; then \
        echo "git $have is older than $GIT_FLOOR" >&2; exit 1; \
    fi; \
    ln -s "$(command -v fdfind)" /usr/local/bin/fd

# just is not packaged for bookworm; use the official prebuilt release.
RUN set -eux; \
    case "$(uname -m)" in \
        x86_64) arch=x86_64 ;; \
        aarch64) arch=aarch64 ;; \
        *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/casey/just/releases/download/${JUST_VERSION}/just-${JUST_VERSION}-${arch}-unknown-linux-musl.tar.gz" \
        | tar -xz -C /usr/local/bin just; \
    just --version

RUN set -eux; \
    curl -fsSL "https://astral.sh/uv/${UV_VERSION}/install.sh" \
        | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh; \
    uv --version

RUN npm install -g @anthropic-ai/claude-code && claude --version

COPY --from=builder /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/home/agent/go/bin:$PATH
COPY --from=builder /out/sweatshop /usr/local/bin/sweatshop
COPY --chmod=0755 station/entrypoint.sh /usr/local/bin/station-entrypoint

RUN useradd --create-home --uid 1000 --shell /bin/bash agent \
    && mkdir -p /work && chown agent:agent /work
USER agent
WORKDIR /work

# CMD, not ENTRYPOINT: the check runner runs `<image> bash -c <check>`, and the
# Maker runner passes no arguments so the station entrypoint runs.
CMD ["/usr/local/bin/station-entrypoint"]
