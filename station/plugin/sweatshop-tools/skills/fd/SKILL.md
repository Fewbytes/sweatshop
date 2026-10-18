---
name: fd
description: Find files and directories by name with fd. Use instead of find.
---

# fd

```
fd 'regex'                # names matching a regex, ignores .gitignore'd paths
fd -e go                  # by extension
fd -e go -E testdata      # exclude a path
fd -t d name              # directories only (-t f files)
fd -g '*_test.go'         # glob instead of regex
fd -H -I name             # include hidden and ignored files
fd -e md -x wc -l         # run a command on each match
```

`fd` is installed as `fd` (Debian packages it as `fdfind`; the image links it). To search contents use `rg`.
