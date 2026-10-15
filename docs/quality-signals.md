# LLM-era Quality Signals and How They Are Measured

What the line measures about code, and how each number is computed. Rationale
and evidence: [`research-docs/llm-code-quality/README.md`](../research-docs/llm-code-quality/README.md)
section 5. The rules these signals check: [style.md](style.md) and the
[kaizen guide](../line/guidance/kaizen.md) section 3.

The signals are proposed for this project, not established in the literature.
Read them as trend indicators on a fixed repo, not as scores to compare across
repos. Each signal is deterministic given the repo at a commit, so a change in
the number is a change in the code.

## Defaults

| Parameter | Default | Used by |
|---|---|---|
| Co-change window | last 200 commits | hidden-coupling ratio |
| Support | 5 (a pair must co-change in at least 5 commits) | hidden-coupling ratio |
| Confidence | 0.6 (see below) | hidden-coupling ratio |
| Static edges | import and call edges | hidden-coupling ratio, call depth |
| Idiom rules | ast-grep rules, maintained by kaizen | pattern entropy, dynamic-dispatch sites |
| Tokenizer | `cl100k_base` | context closure |
| Closure | function body + direct callees + direct types | context closure |

## signals.json (schema 2)

One adapter per language (`line/kaizen/signals/adapter.py`) declares the signals
it computes and returns, for each, a number or `{"unavailable": reason}`.
`tools` records every tool and rule-set version the numbers depend on (git,
python, the Go adapter's version). Two runs with different `tools` (or a schema 1
run, which recorded none) are different baselines: `delta` refuses to compare
them and the report shows no trend across them. Bump `GO_ADAPTER_VERSION` in
`go_adapter.py` when a change to the lexer or idiom table moves numbers.

## Hidden-coupling ratio (lower is better)

Files that change together but have no static link force a reader to remember
"also update X" (writer guide, section 2).

1. Take the last 200 commits. Per commit, collect the set of changed files.
2. A pair (A, B) is a co-change pair when both change together in at least 5
   commits (support) and `commits(A and B) / commits(A)` or the same ratio over
   `commits(B)` is at least 0.6 (confidence).
3. A pair has a static edge when either file imports the other, or a function
   in one calls a function in the other.
4. Ratio = co-change pairs with no static edge ÷ all co-change pairs.

Raising support or confidence shrinks the pair set and makes the ratio noisier;
keep the defaults when comparing runs. Fix for a high pair: make the link
explicit (shared constant or type), co-locate, or add a test that fails on
divergence (kaizen P1).

## Context closure (lower is better)

The tokens an agent must read to understand one function.

1. Resolve the function's direct callees and the direct types in its signature
   and body.
2. Concatenate the source of the function body, those callees and those types.
3. Count tokens with `cl100k_base`.

Closure is one hop deep on purpose: a transitive closure grows with the whole
call graph and stops distinguishing functions. A model-specific tokenizer would
give different absolute numbers, so the tokenizer is fixed; compare only
numbers produced with the same one. Reported per touched function.

## Idiom signals (lower is better)

- **Pattern entropy:** per concern (error handling, I/O, config, logging), the
  count of distinct idioms found by ast-grep rules. One idiom is the goal.
- **Dynamic-dispatch sites:** count of ast-grep matches for reflection, string
  dispatch and implicit registration.

The rules live with kaizen, which adds or retires them as it finds new idioms.
A rule change moves the number without any code change, so record the rule set
version with a reported value.

## Signals measured elsewhere

These need no custom tooling; the kaizen guide table names the tool for each:
name collisions (symbol index), call depth (call graph), import cycles
(dependency graph), dead symbols, typed-boundary ratio, mutation score, and
test runtime (`just test`).

## Not yet measured

Spec density, verification latency, doc-code drift and the agent-efficiency
benchmark from research section 5 have no implementation.
