# Threat model

Status: initial `v0.1-private` threat model, 2026-07-26.

This model covers the current read-only npm + OCI service and the planned
private-use hardening work. It is not a security certification.

## Assets

- Upstream credentials and bearer tokens.
- Private package names, manifests, distribution files, and image layers.
- Cache integrity and the association between a request and cached response.
- Configuration, logs, metrics, and status output.
- Availability of builds and deployments that depend on n0ding.

## Trust boundaries

```text
package client
  |  untrusted request headers and credentials
  v
n0ding adapter
  |  shared HTTP/cache policy
  |  repository-specific validation
  v
local cache filesystem  <---->  configured upstream over HTTP(S)
  |
  `--> status, metrics, and logs visible to the private operator network
```

The private network is not assumed to make every client trustworthy. A
compromised CI runner or workstation can send crafted headers and paths.
Configured upstreams can be unavailable, compromised, or return
user-specific responses.

Local users who can read the cache directory can read cached artifacts. Local
users who can modify it are outside the current integrity boundary.

## Threats, current controls, and gaps

| ID | Threat | Current control | Remaining proof or gap |
|---|---|---|---|
| T1 | Client credential forwarded to the wrong upstream | Shared header policy strips unsupported identity headers; `Authorization` needs adapter opt-in and survives only exact-origin redirects; full-server npm/OCI fixtures prove cross-origin and failing redirects are credential-free | Real registry/CDN redirect behavior and supported private-auth schemes remain unverified |
| T2 | Credential persisted in cache metadata | Authentication metadata is rejected; cookie-bearing responses require explicit `public`; the storage layer scrubs known sensitive fields again; raw fixtures, stopped backup/restore evidence, and the retention-smoke cache/archive/operator outputs pass token/query/response canary scans | Repeat the raw scan against real private upstream output |
| T3 | Credential exposed through status, errors, or logs | Status and explicit upstream/error URL fields omit userinfo, query, and fragment; full-server and retention-smoke evidence scans status, metrics, structured logs, expected cancel errors, client results, and progress/result artifacts | Arbitrary paths and nested non-URL error strings are not a secret store; real-upstream scan remains pending |
| T4 | User-specific body reused for another client | npm authenticated requests bypass shared caching; two-identity fixtures create zero npm objects; OCI serves shared bytes only after exact current-identity digest confirmation, differing/missing digests force a fresh `GET`; restore and concurrent retention-smoke cycles repeat A/B/denied checks | Test two real identities and provider revocation; OCI authenticated content sharing remains a deliberate sensitive boundary |
| T5 | Wrong representation reused because cache key is incomplete | Cache keys include target and `Accept`; responses varying on any other client-controlled dimension are not stored, except fixed upstream `Accept-Encoding: identity` | Extend the key only with evidence from a protocol adapter |
| T6 | Partial or corrupt object committed or restored | Atomic commit, size check, OCI SHA-256 verification, temp cleanup; missing/malformed/size-mismatched restored pairs are not counted or served; restore and retention smoke prove truncated-body refetch, canceled-stream cleanup, SIGKILL temp cleanup, and final body/metadata integrity | npm body integrity is ultimately enforced by npm/lockfile SRI; an unavailable upstream prevents repair; seven-day evidence remains pending |
| T7 | Malicious upstream poisons clients | Configured HTTPS upstream, OCI digest checks, npm client integrity where present | No signature, provenance, malware, or policy verification |
| T8 | Unauthorized client reads private cache | No n0ding client authentication exists | Bind privately and enforce TLS/auth at a reverse proxy; private-use auth topology needs a drill |
| T9 | Disk exhaustion or deletion races | Age-based GC deletes only complete objects and ignores active temps; store locking protects local commits; the short soak proves forced expiry to zero complete objects, disk reduction, refetch, concurrent writers, and status/metrics consistency; the private-alpha decision requires dedicated capacity plus host free-space alerts | Age alone does not bound burst ingress, in-flight temps, malformed/orphaned bytes, or total filesystem use; no strict byte quota exists; the real seven-day run and per-deployment capacity/risk-acceptance drill remain pending |
| T10 | Backup captures inconsistent state or secrets | Stopped Compose backup includes cache and config; restore targets a fresh empty volume; archive members, cache/status/metrics/log/error outputs, restored identity behavior, rollback, timings, and canaries are checked deterministically in CI | Cross-version format compatibility and backup of real private-upstream output remain unverified |
| T11 | Multiple writers corrupt storage | Documented one-process/one-writable-cache rule | No distributed lock; deployment must enforce the rule |
| T12 | SSRF through client-controlled target | Adapter constructs initial targets under one configured upstream; redirect credentials are exact-origin only; deterministic cross-origin success and failure redirects strip credentials | Redirect destinations remain upstream-controlled and are not allowlisted; real provider chains remain pending |

## Cache-admission policy

n0ding is a shared cache. It conservatively refuses persistent storage when:

- the client requests `no-cache`, `no-store`, `max-age=0`, or legacy
  `Pragma: no-cache`;
- the upstream response contains `private`, `no-store`, or `no-cache`;
- the response contains authentication metadata;
- the response contains cookies without explicit `Cache-Control: public`;
- `Vary` names a request field that the adapter's cache key does not cover.

Cookie fields are removed from persistent metadata even when an explicitly
public response is cacheable. This preserves public registries that attach an
edge cookie without replaying that cookie to another client.

The current adapters cover `Accept`; upstream requests force
`Accept-Encoding: identity`, so that dimension is fixed. This policy is a
minimum safety rule, not a complete implementation of
[RFC 9111 HTTP caching](https://datatracker.ietf.org/doc/html/rfc9111).

## Private-upstream credential rules

Until Section 2 of the [private roadmap](release-checklist.md) is complete:

- npm `forward_authorization = false` remains the safe default;
- enabling it forwards only the `Authorization` header and makes that request
  bypass the shared npm cache; fixture identities A and B cannot populate or
  consume a persistent private npm entry;
- OCI forwards `Authorization` because the Registry V2 pull flow requires it,
  never writes that header to cache metadata, and validates every hit upstream
  against the exact cached digest for the current identity;
- cookie-based, OTP-based, proxy-based, and custom-header authentication are
  unsupported;
- redirects retain `Authorization` only for the exact same scheme, host, and
  effective port; cross-origin redirects remain allowed without client
  credentials, and redirect userinfo is discarded;
- credentials embedded in a configured URL remain present in the config and
  process memory even though status/log display is sanitized.

The automated full-server fixture also proves immediate local token revocation
without a n0ding restart. It does not prove compatibility with a specific
private npm service, OCI registry, identity provider, provider token
revocation/expiry model, or Basic-auth challenge flow. The reproducible real
service procedure is [private-upstream-drill.md](private-upstream-drill.md).

## Review triggers

Review this model before:

- implementing the aggregate byte-limit design in
  [retention-policy.md](retention-policy.md);
- implementing private-upstream credential configuration;
- adding PyPI;
- changing the cache key or response-header policy;
- adding client authentication, publishing, shared storage, or a database;
- claiming availability or supply-chain security properties.
