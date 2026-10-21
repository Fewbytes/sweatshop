# Code tooling

The tools the line and its agents use to check, search and navigate code. Each
section says what the tool is for, how to run it, and where it is configured.
Recipes are in the root [`justfile`](../justfile); run `just` to list them.

## Quality checks

| Check | Purpose | Run | Configured in |
|---|---|---|---|
| `go vet` | Suspicious constructs in Go | `just lint` | `lint` recipe in `justfile` |
| `gofmt` | Formatting; fails if any file would change | `just lint` | `lint` recipe |
| `go mod tidy` | `go.mod`/`go.sum` match the imports; fails on drift | `just lint` | `lint` recipe |
| `go test` | Tests of all four modules (`agentsh`, `bughunt`, `depmaint`, `line`) | `just test` | `test` recipe |
| marketplace validation | Plugin manifest and package layout | `just validate-marketplace` | `scripts/validate-marketplace.mjs` |
| station tools check | Fails when a tool in this document is missing from the station image | `just station-tools` (inside the image only) | `station-tools` recipe |

`just ci` runs all of them (`lint test build validate-marketplace
station-tools`). It is the line's check: the witness runs it in the station
image, and the merge guard lands a commit only after it passes. GitHub Actions
(`.github/workflows/ci.yml`) runs `lint`, `test`, `build` and
`validate-marketplace` on every push and pull request.

## Static analysis (bughunt)

`bughunt` runs Go detectors (`staticcheck`, `errcheck`, `ineffassign`, `gosec`)
and the static rules it learns from confirmed bugs. `bughunt scan --diff <rev>`
reports findings on changed lines only.

- Pre-commit gate: `just bughunt-hook` (installs `bughunt/scripts/pre-commit`).
- CI: the `bughunt` job in `.github/workflows/ci.yml` installs the detectors,
  runs `bughunt init` and `bughunt scan --diff origin/<base>` from `agentsh/`.
- Spec: [`design-docs/bughunt-spec.md`](../design-docs/bughunt-spec.md).

## Code structure: ast-grep

`ast-grep` (alias `sg`) matches code by syntax, so it finds calls, declarations
and patterns that text search gets wrong. Example:

```bash
ast-grep run --lang go --pattern 'fmt.Errorf($$$)' line/
```

It is installed in the station image from a pinned, checksum-verified release
(`station/Dockerfile`). The `ast-grep` skill in the station plugin teaches
agents to use it. There is no repo-level `sgconfig.yml`; the kaizen idiom rules
that use ast-grep are described in [quality-signals.md](quality-signals.md).

## Language server: gopls

`gopls` answers references, definitions and call-hierarchy questions for Go.
It is built from source at a pinned version in `station/Dockerfile` and wired
into Claude Code by `station/plugin/sweatshop-tools/.lsp.json` (command
`gopls serve`, `.go` files). The entrypoint loads the plugin with
`--plugin-dir` (`station/entrypoint.sh`). Kaizen also drives gopls (and the
Python and TypeScript servers) for dead symbols, call depth and the
typed-boundary ratio (`line/kaizen/signals/lsp.py`).

## Search

| Tool | Finds | Example |
|---|---|---|
| `rg` (ripgrep) | Text, identifiers, regexes in file contents | `rg -n 'func Merge' line/` |
| `fd` | Files and directories by name | `fd -e go guard` |
| `ast-grep` | Code by syntax (see above) | |
| `ctags` (universal-ctags) | Where a symbol is defined | `ctags -R -f tags line/` |

Stations should use these instead of `grep` and `find`. The
`choose-a-search-tool` skill says which fits a question; skills for `rg`, `fd`,
`ctags` and `ast-grep` live in `station/plugin/sweatshop-tools/skills/`. The
station image also carries `jq`, `tree` and `shellcheck`. All of these are
checked by `just station-tools`, so an image build fails if one is missing.

## Quality signals

Kaizen measures code health (clones, complexity, dead symbols, call depth,
hidden coupling, context closure and more) with tree-sitter and the language
servers. Run:

```bash
uv run --no-project --with-requirements line/kaizen/signals/requirements.txt \
    line/kaizen/signals/kaizen_signals.py measure --root . --rev HEAD --out signals.json
```

The same script has `report`, `gate`, `delta`, `mutation`, `rework`, `modules`
and `contracts` subcommands; its docstring lists them. Grammars are pinned in
`line/kaizen/signals/requirements.txt`; per-language tables are
`lang_go.py`, `lang_python.py` and `lang_typescript.py`. The kaizen station runs
this on a schedule (`sweatshop line kaizen`). Definitions of every signal:
[quality-signals.md](quality-signals.md); the rules they check:
[style.md](style.md).
