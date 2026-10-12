# Testing Guide

How to decide what to test, at which level, and what a test must prove. It
agrees with the [writer guide](../line/guidance/writer.md) (every behavior
change ships with a test that fails without it; assert behavior, not
implementation) and the [kaizen guide](../line/guidance/kaizen.md) (refactor
only under tests that would catch a behavior change; weak assertions are a
P9 finding). Where it adds to them, it says so.

The methodology is the king's (2026-10-12): tests are high level, and each
test proves something not already proved elsewhere.

## 1. Test the feature, not the plumbing

Structure code as ports and adapters (hexagonal architecture): feature logic
in the middle, behind interfaces for everything it talks to (HTTP, git,
containers, cards, clocks). Then:

- **Adapters are proved once.** One test shows an HTTP request reaches the
  handler and the response is encoded correctly. After that, feature tests do
  not need to go through the HTTP server. They call the feature through its
  port and prove the logic works.
- **Do not re-prove what is already proved.** If routing, auth or
  serialization has its own test, a feature test that repeats it through the
  full stack adds cost and a second reason to fail, not confidence.
- **Ask what the test proves.** Before writing a test, state in one sentence
  the claim it establishes. If an existing test already establishes it, do not
  write another. If the claim is about plumbing (a request reaches a handler),
  test the plumbing once, at the adapter.

## 2. Pick the level

Prefer the highest level that is still fast and hermetic, and go no lower than
one level below integration.

| Level | Use for | Example in this repo |
|---|---|---|
| End to end | A user-visible workflow across real components | `TestAndonStopFixClear` in `line/harness/andon_test.go`: real station API, witness, guard and watcher through break, stop, fix, clear |
| Integration | Several real components of one feature, fakes only at the outer edge | `line/station/*_test.go`: station routes against a fake card store |
| One level below | A feature's logic through its port, adapters faked | a station feature driven through its Go interface with a hand-written fake, no HTTP server |
| Unit | A pure function with real branching (parsing, validation, encoding) | `line/attest/dsse_test.go` |

Do not write a unit test per function or per file to mirror the code layout.
Such tests pin the implementation, break under correct refactors, and make
kaizen harder. A function with no behavior of its own is covered by the
feature test that uses it.

## 3. What a good test looks like

- **Name states behavior:** `TestAndonStopFixClear`, not `TestAndon2`.
- **Fails without the change.** For a bug fix, write the test first and see
  it fail on the unfixed code.
- **Asserts outcomes** (state, return values, what landed in the fake), not
  calls or internal structure. `!= nil` and "no error" alone are not
  assertions of behavior.
- **Hermetic and deterministic:** `t.TempDir()`, `httptest`, injected clocks,
  no network, no shared global state, no sleeps for ordering. Runs under
  `just test` with no setup.
- **Fakes live at ports, written by hand** and local to the test package
  (see `fakeCards` in `line/station/cards_test.go`). No mocking framework,
  no reflection.
- **Co-located** with the code (`foo_test.go` beside `foo.go`). Cross-package
  end-to-end tests sit in the package that owns the workflow (`line/harness`).
- **Failures are loud and specific:** the message says what was expected and
  what happened.
- **Adversarial cases are first-class** where input is untrusted (guards,
  attestations, anything reading cards or telemetry): test the rejection, not
  only the accept path.

## 4. Slow and external tests

Tests needing a container engine, a real network or minutes of time are
integration tests of the outer edge. Mark them `//go:build !short` (as in
`agentsh/internal/integration`) and keep their number small; the feature
logic behind them is tested at the lower levels above.

## 5. Who may change which tests

Tests pin meaning, and the line treats them accordingly:

- Makers add and edit tests together with the change that needs them.
- Never weaken a test (remove an assertion, loosen a tolerance, skip it) to
  make a change pass. If a test is wrong, say why in the commit and card.
- Kaizen deletes redundant tests only when another test proves the same claim.
- Acceptance tests, which define "works" from outside, are owned by gemba; see
  the gemba testing guide (card `sweatshop-u1g`) and
  [`design-docs/gemba-agent.md`](../design-docs/gemba-agent.md). This guide
  covers how to write tests; that one covers which tests are acceptance tests
  and who may change them.

## 6. Checklist

- [ ] I can state in one sentence what each new test proves.
- [ ] No existing test already proves it; adapters are tested once, not per feature.
- [ ] The test is at the highest fast, hermetic level, not a per-function mirror.
- [ ] It fails without my change.
- [ ] It asserts outcomes, with a behavior-stating name.
- [ ] `just ci` passes.
