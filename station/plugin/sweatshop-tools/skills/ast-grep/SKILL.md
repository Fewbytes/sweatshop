---
name: ast-grep
description: Search and rewrite code by syntax with ast-grep (sg). Use for calls, declarations and patterns that text search gets wrong.
---

# ast-grep

Matches syntax trees, so it ignores formatting, comments and strings. `$X` matches one node, `$$$X` matches many.

```
ast-grep run -l go -p 'fmt.Errorf($$$A)'                  # find every call
ast-grep run -l go -p 'if err != nil { return err }'       # find a statement shape
ast-grep run -l python -p 'def $F($$$P): $$$B'              # Python functions
ast-grep run -l go -p '$A.Close()' --json                   # machine-readable
ast-grep run -l go -p 'old($X)' -r 'new($X)' -U             # rewrite in place (-U applies)
ast-grep run -l go -p 'old($X)' -r 'new($X)'                # preview the rewrite
ast-grep scan                                               # run rules from sgconfig.yml
```

`sg` is the same binary. Pass `-l` (language) every time. Preview a rewrite before `-U`, then run the tests. For plain text use `rg`.
