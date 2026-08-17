# agentsh context benchmark

This benchmark compares a fixed repository task executed by Claude Code with
built-in Bash versus the agentsh MCP `Bash` tool. Use the same model, prompt,
repository snapshot, permissions, and temperature. Run each condition at least
10 times after discarding warm-up runs.

Record for every run:

- input/output token counts reported by the host
- wall-clock duration
- command count and exit failures
- number of `BashOutput` pages/grep calls
- task success and final diff hash

The headline metric is median output tokens injected into the model context;
secondary metrics are total tokens, duration, and success rate. Do not compare
runs with different prompts or tool availability. Store raw host telemetry
outside the repository when it contains credentials or repository content.

Suggested task: inspect a repository, locate a deliberate failing test, fix it,
run the focused test and full test suite, and summarize the diff. The task must
require enough output to exercise paging but have a deterministic final diff.
