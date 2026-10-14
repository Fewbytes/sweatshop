# Testing Guide

How to decide what to test, at which level, and what a test must prove. It
agrees with the [writer guide](writer.md) (every behavior
change ships with a test that fails without it; assert behavior, not
implementation) and the [kaizen guide](kaizen.md) (refactor
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

## 4. Make tests deterministic with the harness

"No sleeps" is the floor. A test is deterministic when every source of
variation is an input the test controls. Give the code a port for each, and
let the harness (or the test's fake) supply the controlled version:

- **Logical clocks.** Code takes a `now func() time.Time` or a clock
  interface, never calls `time.Now` or `time.Sleep` itself. The test owns a
  fake clock and moves it with an explicit `Advance(d)`. Timeouts, liveness
  limits and backoff are then asserted by advancing the clock past the limit,
  in microseconds, not by waiting. See `MaxClockSkew` and the heartbeat clock
  in `line/station/liveness.go`.
- **Pseudo-random with fixed seeds.** Code takes a `*rand.Rand` (or a
  source) from its caller. Tests seed it with a constant written in the test,
  so a failure replays exactly. Randomized or property tests print the seed
  on failure.
- **Injected IDs.** Card IDs, run IDs, nonces and temp names come from an
  injected generator that the test makes sequential (`run-1`, `run-2`).
  Assertions then compare to literal expected values instead of patterns.
- **Controlled scheduling.** Do not rely on goroutines racing in the right
  order. Make concurrency a step the test drives: run the worker's one
  iteration on the test goroutine, or have the fake block on a channel the
  test releases. Wait on a condition (channel, `sync.WaitGroup`, a
  state-change callback), never on elapsed time. Two racing makers in
  `TestLostRaceRebasesAndRetries` are ordered by the test's own calls, not by
  timing.
- **Everything else that varies:** map iteration order (sort before
  asserting), the filesystem (`t.TempDir()`), environment (`t.Setenv`),
  locale and time zone (set explicitly), and ports (`httptest`, not fixed
  numbers).

A test that fails one run in a hundred is a bug in the test or in a missing
port; fix the cause, never add a retry or a longer wait. Where a test must
cope with a loaded machine, widen a deadline in the fake clock, not a real
sleep.

## 5. Write tests in the language of the business

A test reads best when it says what happens in the domain: a maker's change
is merged, a stopped line rejects it, a forged witness is refused. Wiring
(temp dirs, keys, git plumbing, JSON) is noise that fills the context of
whoever reads or edits the test and invites copy-paste of the wrong things.

So give each package that owns a workflow a small test SDK: helpers named for
business actions and outcomes, built once on the harness.

- **Actions are verbs of the domain.** `l.agent("maker-1", "maker")`,
  `a.change("f.txt", "x")`, `a.merge()`, `a.rebase()` in `line/harness`
  hide repository setup, signing and submission. Add `l.pullCord()` or
  `l.fileCard(...)`, not a block of HTTP calls inline.
- **Assertions are domain outcomes.** `expect(t, result, guard.Rejected,
  guard.ReasonBaseMoved)` states the business result. Prefer helpers that
  say `requireMerged`, `requireCardClosed` over comparing raw fields.
- **A test body is a short story**: arrange the line, act as an agent,
  assert the outcome. If a test needs more than a few lines of plumbing,
  the SDK is missing a verb; add it where all tests in the package can use it.
- **The SDK is the harness's public surface.** It fails loudly (helpers call
  `t.Fatal` with what was expected and what happened), takes the clock, seed
  and ID generator from section 4 as constructor options, and has defaults
  that make the common test one line.
- **Keep it honest.** SDK helpers contain no assertions about unrelated
  behavior and no logic worth its own bugs; if a helper needs a test, it is
  too clever. Adapters are still proved once (section 1); the SDK drives the
  feature through its port.
- **One SDK per workflow, not per test file.** Before adding a helper,
  search the package for an existing one. Do not grow a second vocabulary
  for the same action.

## 6. Slow and external tests

Tests needing a container engine, a real network or minutes of time are
integration tests of the outer edge. Mark them `//go:build !short` (as in
`agentsh/internal/integration`) and keep their number small; the feature
logic behind them is tested at the lower levels above.

## 7. Who may change which tests

Tests pin meaning, and the line treats them accordingly:

- Makers add and edit tests together with the change that needs them.
- Never weaken a test (remove an assertion, loosen a tolerance, skip it) to
  make a change pass. If a test is wrong, say why in the commit and card.
- Kaizen deletes redundant tests only when another test proves the same claim.
- Acceptance tests, which define "works" from outside, are owned by gemba; see
  the gemba testing guide (card `sweatshop-u1g`) and
  [`design-docs/gemba-agent.md`](../../design-docs/gemba-agent.md). This guide
  covers how to write tests; that one covers which tests are acceptance tests
  and who may change them.

## 8. Checklist

- [ ] I can state in one sentence what each new test proves.
- [ ] No existing test already proves it; adapters are tested once, not per feature.
- [ ] The test is at the highest fast, hermetic level, not a per-function mirror.
- [ ] Clocks, randomness, IDs and scheduling are injected; no sleeps.
- [ ] The test body reads as business steps; plumbing lives in the package's test SDK.
- [ ] It fails without my change.
- [ ] It asserts outcomes, with a behavior-stating name.
- [ ] `just ci` passes.
