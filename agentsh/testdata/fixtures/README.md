# agentsh fixtures

Fixtures cover prompt blocking, large output, slow trickle output, escaped
process descendants, signals, and separated stdout/stderr. Invoke them through
`Bash`/the executor rather than directly. Linux cgroup tests require a writable
delegated cgroup v2 hierarchy and are skipped when unavailable.
