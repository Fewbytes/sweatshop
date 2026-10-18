---
name: choose-a-search-tool
description: Pick the right search tool: text, file names, code structure or symbols. Use before reading files by hand, and instead of grep or find.
---

# Choose a search tool

Never use `grep` or `find`; the station has faster tools that respect `.gitignore`.

| You want | Use | Skill |
|---|---|---|
| a string or regex in file contents | `rg` | `rg` |
| files by name or extension | `fd` | `fd` |
| code by structure (calls, declarations, patterns) or a rewrite | `ast-grep` (`sg`) | `ast-grep` |
| where a symbol is defined | `ctags` tags index, or gopls for Go | `ctags` |
| references, types, diagnostics of Go code | the gopls language server (loaded for `.go` files) | none |

Search first, then read only the lines you need (`sed -n 'A,Bp'` or the Read tool with an offset). Do not read whole files to find one function.
