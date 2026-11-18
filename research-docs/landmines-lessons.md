# Lessons from interfere-inc/landmines

King's request (2026-11-18, card sweatshop-dyz2): learn from
https://github.com/interfere-inc/landmines, a set of Oxlint rules against
AI-code bad habits. Each rule is recorded as adopted, adapted or rejected.
Adopted and adapted rules live in `line/guidance/writer.md` and
`line/guidance/testing.md`.

| Rule | Decision | Reason |
|---|---|---|
| `no-test-doubles` (no mocks, stubbed globals, patched prototypes) | Adapted | Go has no module mocks; the guide already demands hand-written fakes at ports. Added: no patching methods, globals or package variables to force an outcome; add a port. Observation-only recording stays allowed. |
| `strict-msw` (unmocked requests must error) | Adapted | Go equivalent is `httptest` with no network, already required ("hermetic"). No MSW here, so no new rule. |
| `no-fixed-sleep` | Adopted | Already in writer and testing guides; kept as is. |
| `no-unsignaled-poll` | Adapted | Added: wait on a signal; poll with `testwait.Until` only when the code under test offers none, and never on state the test owns. |
| `no-flake-masking` (retries, raised timeouts) | Adopted | Testing guide already forbade retry and longer waits; now also names rerun loops and raised timeouts. |
| `no-comments` (prose comments banned) | Rejected | The writer guide wants "why" comments and docstrings on exported symbols; a ban would delete rationale. The "what" ban already exists. |
| `colocated-tests`, `test-file-names` | Rejected | Go enforces `_test.go` beside the code and the guide already requires co-location. Nothing to add. |
| `package-exports` | Rejected | TypeScript package.json concern; Go visibility is by identifier. |
| `no-explicit-return-types` | Rejected | Opposite of the writer guide, which wants precise explicit types for verifiability, and Go requires them. |
| Error messages state the honest alternative | Adopted | Added to the writer guide as a rule for code and checks we write. |
| Per-file shrinking allowlists with exact counts | Adopted | Added to the writer guide as the way to handle legacy violations of a check. `line/testhygiene/analyzer_allowlist.txt` is the first such list. |
