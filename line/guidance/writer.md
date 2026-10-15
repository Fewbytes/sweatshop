# Writer Guide: Code for LLM Readers

For agents that write new code or change existing code. Rationale and evidence:
`research-docs/llm-code-quality/README.md`.

Your output will be read mostly by other agents, which find code by searching
it, check it with tools, and copy whatever patterns they see. Humans read the
review surface: interfaces, tests, diffs. Write for both, in that order of
volume.

Three goals, in priority order:

1. **Verifiable** — a change can be checked mechanically, fast.
2. **Locatable** — a reader can find everything it needs to read with search
   and static navigation, and nothing it doesn't.
3. **Imitable** — the patterns you leave are ones you want copied.

---

## 1. Verifiability

- **Encode claims in checkable form.** Prefer, in order: type > assertion /
  contract > test > comment. If a comment states an invariant, ask whether a
  type or assert could state it instead.
- **Use the strongest types the language offers.** Precise parameter and
  return types, enums/sum types over strings and bools, newtypes for IDs and
  units. Avoid `any`, `interface{}`, untyped dicts across module boundaries.
- **Make illegal states unrepresentable** where cheap. Parse at the boundary,
  pass validated types inward.
- **Every behavior change ships with a test** that fails without it. Tests
  must be fast, deterministic, hermetic and runnable with one command.
- **No wall-clock waits in tests.** No `time.Sleep` and no shell `sleep`. Wait
  on something the test controls: an injected clock or ticker, a channel or
  callback, or `testwait.Until` (`agentsh/internal/testwait`), which polls with
  a deadline and fails with a clear message. For process tests, run a command
  that blocks on a pipe or file the test releases.
- **Assert behavior, not implementation.** A test that would survive a correct
  refactor but catch a wrong one. Weak assertions (`!= nil`, "no error") do
  not count.
- **Fail loudly.** No swallowed errors, no silent fallbacks, no default values
  that hide a missing input. An error is a signal the next agent needs.

## 2. Locatability

- **Names are the index.** Every exported symbol gets a name that is
  descriptive, domain-specific and unique in the repo. Before introducing a
  name, search for it; if it collides with an unrelated symbol, pick another.
- **Never build identifiers dynamically.** No string-concatenated function,
  route, config key or event names. Write the full literal so search finds it.
- **Explicit over implicit.** Avoid reflection, monkey-patching, magic
  methods, implicit registration, decorators with hidden side effects, DI
  containers, and global mutable state. If dispatch is needed, use a literal
  table that lists every target.
- **Locality over fragmentation.** Keep code that changes together in one
  place. A cohesive 80-line function with linear flow beats five 15-line
  helpers spread over three files that are only called once. Extract a helper
  when it has a name worth reading on its own, is reused, or is tested on its
  own, not because of a line-count rule.
- **Shallow call depth.** Limit hops from entry point to the work. Each layer
  of indirection is a lookup the reader must do.
- **No hidden coupling.** If two places must change together, make the link
  static and visible: shared constant, shared type, a function both call, or
  a test that fails when they diverge. Never rely on "remember to also update
  X".
- **Bounded fan-out.** A module that imports everything is read by every
  change. Keep dependencies few and acyclic; no import cycles.
- **Co-locate tests** with the code they test, following repo convention, so
  the code-to-test link can be found.

## 3. Imitability (pattern hygiene)

- **Read before writing.** Find two or three existing examples of the same
  concern (error handling, logging, config, I/O, HTTP, DB) and follow the
  dominant pattern. Do not add a new idiom for a solved concern.
- **One canonical way per concern.** If you must deviate, replace the old
  pattern everywhere in scope, or file an issue for that. Do not leave two.
- **DRY for knowledge, not for shape.** Business rules, invariants, constants,
  schemas and protocol details must have exactly one source of truth.
  Incidental similarity (two loops that look alike) may stay duplicated; do
  not invent an abstraction to merge them.
- **No copy-paste of blocks over ~5 lines** that encode the same knowledge.
  Extract, or reference the original.
- **No speculative generality.** No unused parameters, options, interfaces
  with one implementation, plugin points or config for hypothetical needs.
  Every abstraction must already pay for itself.
- **Delete what you replace.** No dead code, commented-out code, unused
  exports, or compatibility shims without a stated removal condition.

## 4. Comments and docs

- **Comments say why, never what.** Rationale, constraints, rejected
  alternatives, links to issues or specs. If the code needs a "what" comment,
  rename or restructure instead.
- **A wrong comment is worse than none.** When you change code, update or
  delete every comment, docstring and doc that describes it. If unsure a
  comment is still true, verify it or remove it.
- **Docstrings on exported symbols** state contract: inputs, outputs, errors,
  side effects, invariants. Prefer an executable example (doctest, example
  test) over prose.
- **Do not write narration** ("Now we loop over items", "Added for task X").
  The VCS log holds history.

## 5. Context budget

- **Keep files a size an agent reads in one pass** (rough guide: under ~500
  lines; follow the repo norm). Split by cohesion, not by line count.
- **Keep the context closure small.** To understand a function, the reader
  should need its body, its direct types and few direct callees, not a deep
  transitive tree.
- **Avoid long generated or vendored blobs** in source directories; keep
  them out of the search path.

## 6. Review surface (for humans)

- **Small, single-purpose diffs.** One logical change per commit. Mechanical
  changes (renames, moves, formatting) in separate commits from behavior
  changes.
- **Interfaces and tests are the human-readable spec.** Spend your
  readability effort on public signatures, type definitions, test names and
  test bodies.
- **Test names state behavior:** `TestRetryStopsAfterMaxAttempts`, not
  `TestRetry2`.

---

## Pre-submit checklist

- [ ] Types precise at every new boundary; no new `any` / untyped maps
- [ ] New behavior has a failing-before, passing-after test
- [ ] Every new exported name searched for; unique in repo
- [ ] No dynamic dispatch by string, no reflection, no new hidden registration
- [ ] Followed the existing idiom for each concern touched; no second idiom
- [ ] No knowledge duplicated; no speculative abstraction
- [ ] Every comment/doc describing changed code updated or removed
- [ ] Replaced code deleted; no dead code left
- [ ] Linters, type checker, tests pass locally
- [ ] Diff is single-purpose
