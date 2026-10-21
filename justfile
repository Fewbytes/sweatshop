# Sweatshop build tooling. Run `just` for the recipe list.

agentsh_dir := "agentsh"
bughunt_dir := "bughunt"
depmaint_dir := "depmaint"
line_dir := "line"
bin_dir := "bin"
version_pkg := "github.com/Fewbytes/sweatshop/agentsh/internal/version"
bughunt_version_pkg := "github.com/Fewbytes/sweatshop/bughunt/internal/version"
depmaint_version_pkg := "github.com/Fewbytes/sweatshop/depmaint/internal/version"
version := `git -C . describe --tags --always --dirty 2>/dev/null || echo dev`
commit := `git -C . rev-parse --short HEAD 2>/dev/null || echo none`
ldflags := "-X " + version_pkg + ".Version=" + version + " -X " + version_pkg + ".Commit=" + commit
bughunt_ldflags := "-X " + bughunt_version_pkg + ".Version=" + version + " -X " + bughunt_version_pkg + ".Commit=" + commit
depmaint_ldflags := "-X " + depmaint_version_pkg + ".Version=" + version + " -X " + depmaint_version_pkg + ".Commit=" + commit

# Build agentsh and agentshd into ./bin
build:
    mkdir -p {{bin_dir}}
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o ../{{bin_dir}}/agentsh ./cmd/agentsh
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o ../{{bin_dir}}/agentshd ./cmd/agentshd
    cd {{bughunt_dir}} && go build -ldflags '{{bughunt_ldflags}}' -o ../{{bin_dir}}/bughunt ./cmd/bughunt
    cd {{depmaint_dir}} && go build -ldflags '{{depmaint_ldflags}}' -o ../{{bin_dir}}/depmaint ./cmd/depmaint
    cd {{line_dir}} && go build -o ../{{bin_dir}}/sweatshop ./cmd/sweatshop

# Run the agentsh Go test suite
test:
    cd {{agentsh_dir}} && go test ./...
    cd {{bughunt_dir}} && go test ./...
    cd {{depmaint_dir}} && go test ./...
    cd {{line_dir}} && go test ./...

# go vet, gofmt, and go.mod tidy — all must be clean, none of them modify anything
# further once satisfied (go mod tidy does update go.mod/go.sum in place, so
# this leaves the tree dirty on drift the same way `go mod tidy` always does)
lint:
    cd {{agentsh_dir}} && go vet ./...
    cd {{agentsh_dir}} && test -z "$(gofmt -l .)" || (echo "gofmt needs to run on:" && gofmt -l . && exit 1)
    cd {{agentsh_dir}} && go mod tidy && git diff --exit-code go.mod go.sum
    cd {{bughunt_dir}} && go vet ./...
    cd {{bughunt_dir}} && test -z "$(gofmt -l .)" || (echo "gofmt needs to run on:" && gofmt -l . && exit 1)
    cd {{bughunt_dir}} && go mod tidy && git diff --exit-code -- go.mod go.sum
    cd {{depmaint_dir}} && go vet ./...
    cd {{depmaint_dir}} && test -z "$(gofmt -l .)" || (echo "gofmt needs to run on:" && gofmt -l . && exit 1)
    cd {{depmaint_dir}} && go mod tidy && git diff --exit-code -- go.mod go.sum
    cd {{line_dir}} && go vet ./...
    cd {{line_dir}} && test -z "$(gofmt -l .)" || (echo "gofmt needs to run on:" && gofmt -l . && exit 1)
    cd {{line_dir}} && go mod tidy && git diff --exit-code -- go.mod go.sum

# Build agentsh and agentshd from source and install them to a directory on
# PATH. Requires a Go toolchain (and a C compiler — go-libsql needs CGO).
install dest=(env('HOME') / ".local/bin"):
    mkdir -p {{dest}}
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o {{dest}}/agentsh ./cmd/agentsh
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o {{dest}}/agentshd ./cmd/agentshd
    cd {{bughunt_dir}} && go build -ldflags '{{bughunt_ldflags}}' -o {{dest}}/bughunt ./cmd/bughunt
    cd {{depmaint_dir}} && go build -ldflags '{{depmaint_ldflags}}' -o {{dest}}/depmaint ./cmd/depmaint
    cd {{line_dir}} && go build -o {{dest}}/sweatshop ./cmd/sweatshop
    @echo "Installed agentsh and agentshd to {{dest}} — make sure it's on PATH"

# Download the latest prebuilt agentsh/agentshd release for this platform —
# no Go toolchain needed. See scripts/install-agentsh.sh.
install-release dest=(env('HOME') / ".local/bin"):
    bash scripts/install-agentsh.sh --dest {{dest}}

# Install the bughunt pre-commit gate into this repository's effective hooks
# directory (respects core.hooksPath; does not overwrite an existing hook —
# e.g. the one beads installs).
bughunt-hook:
    #!/usr/bin/env bash
    set -euo pipefail
    repo_root="$(git rev-parse --show-toplevel)"
    hooks_path="$(git config --get core.hooksPath || true)"
    if [ -z "$hooks_path" ]; then
        hooks_path=".git/hooks"
    fi
    case "$hooks_path" in
        /*) ;;
        *) hooks_path="$repo_root/$hooks_path" ;;
    esac
    target="$hooks_path/pre-commit"
    if [ -e "$target" ]; then
        echo "A pre-commit hook already exists at $target — not overwriting it."
        echo "Add the bughunt gate to it by pasting this in:"
        echo
        echo "# bughunt gate"
        echo "command -v bughunt >/dev/null 2>&1 && bughunt scan --diff HEAD"
        exit 0
    fi
    mkdir -p "$hooks_path"
    cp "{{bughunt_dir}}/scripts/pre-commit" "$target"
    chmod +x "$target"
    echo "installed $target — bypass a single commit with --no-verify"

# Validate the plugin marketplace manifest and each plugin's package layout
validate-marketplace:
    node scripts/validate-marketplace.mjs

# Build the station image (Maker container and the line's check runner)
station-image tag="dev":
    docker build -f station/Dockerfile -t sweatshop-station:{{tag}} .

# Build the maker service image (the maker loop; no bd, no claude)
maker-image tag="dev":
    podman build -f station/Dockerfile --target maker -t sweatshop-maker:{{tag}} .

# Build the image a line's services run in (serve, heijunka, andon, kaizen,
# mirror): the sweatshop binary, git and a podman client. Quadlet units use it.
line-image tag="dev":
    podman build -f station/Dockerfile.line -t sweatshop-line:{{tag}} .

# Run a line's maker as a podman service that restarts on failure and with the
# podman machine. The station token and model credential become podman secrets
# read straight from the line dir (never echoed); the maker reaches cards only
# through line serve and starts station containers through the podman socket.
maker-service name="maker-1" line=(env_var("HOME") / ".sweatshop/lines/sweatshop") port="7468" tag="dev":
    tr -d '\n' < {{line}}/stations/{{name}}/token | podman secret create --replace sweatshop-{{name}}-token - >/dev/null
    tr -d '\n' < {{line}}/model.cred | podman secret create --replace sweatshop-model-cred - >/dev/null
    podman rm -f sweatshop-{{name}} >/dev/null 2>&1 || true
    podman run -d --name sweatshop-{{name}} --restart=always \
        --secret sweatshop-{{name}}-token,type=env,target=SWEATSHOP_LINE_TOKEN \
        --secret sweatshop-model-cred,type=env,target=CLAUDE_CODE_OAUTH_TOKEN \
        -e SWEATSHOP_LINE_URL=http://host.containers.internal:{{port}} \
        -e CONTAINER_HOST=unix:///run/podman/podman.sock \
        -v "$(podman info --format '{{{{.Host.RemoteSocket.Path}}' | sed 's|^unix://||')":/run/podman/podman.sock \
        -v {{line}}:{{line}}:ro \
        --security-opt label=disable \
        sweatshop-maker:{{tag}} --dir {{line}} --station {{name}}

# Inside the station image, fail when a search tool or language server is
# missing (the image is the check runner, so this guards every image build).
station-tools:
    #!/usr/bin/env bash
    set -eu
    [ -f /etc/sweatshop/claude-settings.json ] || { echo "not in the station image; skipping"; exit 0; }
    for t in rg fd ast-grep sg ctags jq tree shellcheck gopls; do
        command -v "$t" >/dev/null || { echo "station image lacks $t" >&2; exit 1; }
    done
    rg --version | head -1; fd --version; ast-grep --version; ctags --version | head -1
    gopls version | head -1
    test -f /etc/sweatshop/plugin/sweatshop-tools/.lsp.json
    test "$(ls /etc/sweatshop/plugin/sweatshop-tools/skills | wc -l)" -ge 5

# Everything CI runs
ci: lint test build validate-marketplace station-tools
