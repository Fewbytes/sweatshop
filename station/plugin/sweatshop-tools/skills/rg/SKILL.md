---
name: rg
description: Search file contents with ripgrep (rg). Use to find text, identifiers or regexes in the repository.
---

# rg (ripgrep)

```
rg -n 'pattern'                 # regex, line numbers, ignores .gitignore'd files
rg -n -F 'literal(text)'        # fixed string, no regex
rg -n -t go 'func \(s \*Server\)'  # only Go files (-t py, -t md, ...)
rg -n -g '*.toml' 'timeout'     # glob filter; -g '!vendor/**' excludes
rg -l 'pattern'                 # file names only
rg -c 'pattern'                 # match counts per file
rg -n -C 3 'pattern'            # 3 lines of context
rg -n -w 'name'                 # whole word
rg --files -g '*_test.go'       # list files (like fd, filtered)
```

Search before you read. Use `-t` or `-g` to keep results small. For code structure use the `ast-grep` skill; for a symbol definition use `ctags`.
