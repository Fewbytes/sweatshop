# Sweatshop build tooling. Run `just` for the recipe list.

agentsh_dir := "agentsh"
bin_dir := "bin"
version_pkg := "github.com/Fewbytes/sweatshop/agentsh/internal/version"
version := `git -C . describe --tags --always --dirty 2>/dev/null || echo dev`
commit := `git -C . rev-parse --short HEAD 2>/dev/null || echo none`
ldflags := "-X " + version_pkg + ".Version=" + version + " -X " + version_pkg + ".Commit=" + commit

# Build agentsh and agentshd into ./bin
build:
    mkdir -p {{bin_dir}}
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o ../{{bin_dir}}/agentsh ./cmd/agentsh
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o ../{{bin_dir}}/agentshd ./cmd/agentshd

# Run the agentsh Go test suite
test:
    cd {{agentsh_dir}} && go test ./...

# go vet, gofmt, and go.mod tidy — all must be clean, none of them modify anything
# further once satisfied (go mod tidy does update go.mod/go.sum in place, so
# this leaves the tree dirty on drift the same way `go mod tidy` always does)
lint:
    cd {{agentsh_dir}} && go vet ./...
    cd {{agentsh_dir}} && test -z "$(gofmt -l .)" || (echo "gofmt needs to run on:" && gofmt -l . && exit 1)
    cd {{agentsh_dir}} && go mod tidy && git diff --exit-code go.mod go.sum

# Build agentsh and agentshd and install them to a directory on PATH
install dest=(env('HOME') / ".local/bin"):
    mkdir -p {{dest}}
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o {{dest}}/agentsh ./cmd/agentsh
    cd {{agentsh_dir}} && go build -ldflags '{{ldflags}}' -o {{dest}}/agentshd ./cmd/agentshd
    @echo "Installed agentsh and agentshd to {{dest}} — make sure it's on PATH"

# Validate the plugin marketplace manifest and each plugin's package layout
validate-marketplace:
    node scripts/validate-marketplace.mjs

# Everything CI runs
ci: lint test build validate-marketplace
