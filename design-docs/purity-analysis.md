# Purity analysis (kaizen)

Status: slice 1 implemented, analysis and report only. Card: `sweatshop-id4j`.
Package: `line/purity`. Command: `sweatshop line kaizen purity`.

## Why

Logic that touches no clock, randomness, file, network or global state can be
tested through its port with plain inputs ([testing guide](../line/guidance/testing.md)
sections 1 and 4). Logic that does is hard to test and tends to sit in the same
package as its adapters. The analysis tags every function so Kaizen can see
where that boundary is blurred.

## Approach

Go is the repo's language for the line, so the analysis uses the standard
library's `go/parser` and builds a syntactic call graph; it needs no build, no
type checking and no network, and runs on a clean clone. Python and TypeScript
are not covered. The price of syntax-only analysis is precision, and the
precision rules are chosen so the tags err toward "deterministic" only where
the code is explicitly injected:

- Calls `pkg.F()` are resolved through the file's imports. Calls `F()` resolve
  within the package. Calls `r.m()` resolve only when `r` is the method's own
  receiver. Calls through other values (interfaces, function-typed fields or
  parameters, other receivers) are not followed.
- A function that receives its clock, random source or store as an interface or
  function argument is therefore deterministic from its own point of view.
  That is the hexagonal seam, and the purpose of the tag.
- Only calls count. Mentioning `time.Now` as a value (`now: time.Now` as a
  default for an injected clock) does not.
- Build constraints are ignored: per-platform variants of a function are
  merged. `_test.go` files, `testdata`, `vendor` and hidden directories are
  skipped.

## What is a source

Each is a literal table in `line/purity/sources.go`:

| Source | Meaning |
|---|---|
| `clock` | `time.Now`, `Since`, `Until`, `Sleep`, `After`, `AfterFunc`, `Tick`, `NewTimer`, `NewTicker` |
| `rand` | any call into `math/rand`, `math/rand/v2`, `crypto/rand` |
| `env` | `os.Getenv`, `LookupEnv`, `Environ`, `ExpandEnv`, `Setenv`, `Unsetenv`, `Clearenv` |
| `fs` | any other call into `os` (except inspectors like `os.IsNotExist`), `io/ioutil` |
| `net` | any call into `net`, `net/http`, `net/rpc`, `net/smtp`, `database/sql` |
| `process` | `os/exec`, `os/signal`, `os.Exit`, `os.Getpid` and kin |
| `stdio` | `fmt.Print*`, any call into `log`, `log/slog` |
| `global` | reading or writing a package variable that some function of the package assigns; variables only set at declaration or in `init` (sentinel errors, tables) are constants |
| `arg-mutation` | assigning through a pointer, map or slice parameter (`p.f = v`, `m[k] = v`, `s[i] = v`); a method changing its own receiver is the object's state and is not flagged |

Calls into other packages of the analysed modules are not sources; they
propagate taint as below. Third-party and other standard-library calls are
assumed pure, a known gap (for example `os.File` methods are caught only
through the `os.Open` that produced the file).

## Propagation

A function that contains a source is non-deterministic. A caller of a
non-deterministic function is non-deterministic. Taint is computed by
breadth-first search from the sources over reversed call edges, so each tainted
function carries a shortest `chain` of function IDs down to the function that
touches the source, and `via` names the call or variable there. Everything
else is deterministic. Recursion is handled by the search.

## Architecture report

A package is an **adapter** if at least one of its functions touches a source
directly, otherwise **logic**. The report lists:

- **mixed** packages: adapters that also hold deterministic functions, ranked
  by direct effect count, the candidates for moving logic out;
- **violations**: logic packages importing an adapter package of the analysed
  modules, with the non-deterministic functions they call. `main` packages are
  composition roots and exempt.

## Using it

```
sweatshop line kaizen purity [--root DIR] [--format json|markdown]
sweatshop line kaizen purity --format markdown | sweatshop line kaizen report
```

JSON holds every function, package and violation. The Markdown form is capped
for the report bucket.

## Not in this slice

Enforcement (a gate failing on a new violation), test generation driven by
the tags, and call resolution through types (`go/types`) to follow interface
calls are follow-up cards.
