---
name: ctags
description: Index symbols with universal-ctags and look up where a function, type or method is defined.
---

# ctags (universal-ctags)

Build the index once per checkout, then look symbols up in it:

```
ctags -R --exclude=.git --exclude=node_modules -f .tags .   # build the index (kept out of git: add .tags to .git/info/exclude)
rg -n '^Symbol\t' .tags                                     # where is Symbol defined?
rg -n '^Server\.' .tags                                     # members of a type (Go)
ctags -R --fields=+n --output-format=json -f - . | head      # JSON output
```

Each line is `name<TAB>file<TAB>pattern;"<TAB>kind`. Rebuild after large changes. For Go, gopls (loaded for `.go` files) answers definition and references more exactly; use ctags for other languages and for a quick overview.
