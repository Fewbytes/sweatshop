# deps.dev behavior at scale (sweatshop-nrp)

Measured 2026-10-18 against `https://api.deps.dev` (no auth) from the line
host, with real npm names. Numbers are one-shot samples, not guarantees.

## Findings

| Probe | Result |
|---|---|
| 50 sequential `GET /v3/systems/NPM/packages/{name}` | 5.1 s (~10 req/s), all 200 |
| 400 requests, 16 concurrent | 28 s (~14 req/s), all 200, max latency 3.3 s |
| 400 requests, 64 concurrent | 39 s (~10 req/s), all 200, max latency 13.7 s |
| `POST /v3alpha/versionbatch`, 5000 items | accepted |
| same, 5001 items | HTTP 400 `too many items in request` |
| batch of 2000 distinct versions | 100 responses per page (`nextPageToken`), 20 pages, 5.8 s total |

- No 429 and no `Retry-After` appeared at any tested load. Overload shows up
  as queueing latency (64 workers are slower than 16), not rejection.
- Throughput of single GETs plateaus near 10–14 req/s; more concurrency only
  adds latency.
- Batching exists only for version documents (`versionbatch`, v3alpha): hard
  cap 5000 items per request, fixed page size of 100 results, page through
  with the repeated request body plus `pageToken`. Package version lists
  (`SourceVersions`) and advisories (`SourceAdvisories`) have no batch form.

## Cost for a 500–2000 node graph (cold cache)

- Version lists: one GET per package, 50 s (500 nodes) to 200 s (2000 nodes).
  This dominates and cannot be batched.
- Version documents: ~1 s per 300 nodes through `versionbatch`, versus the
  same 50–200 s as individual GETs.
- Advisories: one GET per distinct advisory id; few relative to nodes.
- Warm cache (TTL 24 h versions, 6 h advisories) costs nothing.

## Verdict: tuning, not architecture

The cache-with-TTL design, the stale-on-failure fallback in `enrich.Fetch`
and the per-source keys all hold. A cold run of a 2000-node graph costs a few
minutes, once per TTL window; no schema or layering change is needed. The
work is in the HTTP layer only: bounded concurrency, retry with backoff, and
(optional) a `versionbatch` fetcher that pre-warms `SourceVersion` rows.

## Recommended policy (for the backoff work)

Precautionary, since no throttling was observed; revisit if 429s are seen.

- Concurrency: at most 8 in-flight requests to deps.dev.
- Retry on transport errors, 429, 500, 502, 503, 504; never on other 4xx.
- Backoff: exponential, base 1 s, factor 2, cap 30 s, full jitter, at most 5
  attempts per request. A `Retry-After` header (seconds) overrides the
  computed delay when larger.
- Per-request timeout 30 s (observed worst case under load: 14 s).
- After retries are exhausted, `enrich.Fetch` already serves the stale copy
  with `RefreshErr`; a cold miss surfaces the error.
- Batch: when pre-warming versions use `versionbatch` with chunks of at most
  1000 items (well under the 5000 cap), following `nextPageToken` until
  empty. Treat as v3alpha: keep the single-GET path as the fallback.
