# Station plugin

`sweatshop-tools/` is a Claude Code plugin baked into the station image at
`/etc/sweatshop/plugin/sweatshop-tools` and loaded by `station/entrypoint.sh`
(`--plugin-dir`). It carries:

- `skills/`: how to use rg, fd, ast-grep and ctags, and which to pick. The
  sources live here so they are versioned and reviewed.
- `.lsp.json`: language servers. gopls serves `.go` files. To add another
  language, install the server in `station/Dockerfile` (pinned, with a
  checksum) and add an entry to `.lsp.json` keyed by language with its
  `command`, `args` and `extensionToLanguage`.

`just station-tools` (part of `just ci` inside the station image) fails when a
tool is missing.
