# Homelab stress benchmark — 2026-08-16

This is a post-release reference benchmark for the `v0.1.0` Public Preview.
It is not a release gate, production-capacity claim, SLA, or comparison with a
repository manager. Results describe one Homelab installation and one remote
load generator.

![Throughput and latency chart](assets/stress-benchmark-2026-08-16.svg)

## Result

The service completed all 30,458 measured requests without an HTTP error,
timeout, process-health failure, or response hash mismatch. Mixed-workload
throughput peaked at about 179 requests/second with 50 concurrent workers.
At 100 workers throughput declined while p99 latency increased, identifying
the practical saturation region for this installation. A single repeatedly
cached npm tarball reached 1,391 requests/second with 200 workers.

| Workload | Concurrency | Requests | Requests/s | p50 | p95 | p99 | Max | Errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Mixed | 5 | 381 | 25.17 | 180 ms | 412 ms | 965 ms | 1,078 ms | 0 |
| Mixed | 10 | 1,062 | 69.83 | 126 ms | 279 ms | 461 ms | 872 ms | 0 |
| Mixed | 25 | 2,534 | 166.60 | 140 ms | 275 ms | 376 ms | 1,112 ms | 0 |
| Mixed | 50 | 2,750 | 179.42 | 253 ms | 621 ms | 809 ms | 1,308 ms | 0 |
| Mixed | 100 | 2,317 | 147.38 | 559 ms | 1,560 ms | 2,120 ms | 4,726 ms | 0 |
| Hot npm tarball | 200 | 21,414 | 1,391.20 | 100 ms | 397 ms | 548 ms | 2,054 ms | 0 |

## Method

- Each mixed level ran for approximately 15 seconds with 5, 10, 25, 50, and
  100 concurrent workers.
- Workers rotated across cached npm metadata, an npm tarball, a PyPI Simple
  JSON page, a PyPI wheel, and an authenticated Docker Hub OCI manifest.
- The hot-artifact phase used 200 workers repeatedly requesting the same
  cached `is-number@7.0.0` npm tarball for approximately 15 seconds.
- Every response body was SHA-256 hashed. Each target produced exactly one
  digest throughout the run, equal to its pre-run baseline.
- The run stopped with successful `/healthz` and `/api/v1/status` responses.
  Repository error counters remained zero.

The server-side counters after the run reported cache-hit ratios of 99.996%
for npm, 99.973% for PyPI, and 99.837% for OCI. Those ratios include benchmark
setup requests and are not used as latency measurements.

## Environment and scope

- One native n0ding process on the project's Homelab preview host.
- Remote load generation over the existing private network path.
- Server cache budget: 1 GiB.
- Runtime reported SHA `2824b31f9caa64f8b78caf1895fe2de8724cb238`.
  The released SHA `12eb4d81ebc0374ee6fa06af6509bc0cdba8c2bc` differs only in
  `.github/workflows/release.yml` and `CHANGELOG.md`; runtime source is
  unchanged.

This benchmark drove real npm, PyPI, and OCI protocol endpoints. It did not
spawn thousands of npm, pip, uv, or Docker CLI processes. Real-client
compatibility, cold/warm behavior, restart persistence, concurrent request
deduplication, cancellation/retry, and cache integrity are covered separately
by the [Public Preview validation](native-public-preview-validation.md) and
[compatibility evidence](compatibility.md).

## Interpretation and limitations

For this host and network path, 25–50 active mixed workers are the useful
operating region observed in this run. This is not a configured limit: other
CPUs, disks, networks, artifacts, upstream behavior, TLS proxies, and cache
states will produce different results.

The benchmark did not capture server CPU, RAM, disk latency, or network
interface utilization. It also does not establish long-duration stability,
availability, multi-host scaling, cold-cache throughput, or production
readiness. Reproduce measurements on the intended deployment before using
them for capacity planning.
