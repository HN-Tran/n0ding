# Security policy

n0ding v0.1.x is a preview for trusted-network evaluation. Do not expose it
directly to the public internet or treat it as a production supply-chain
security control.

## Supported versions

| Version | Security fixes |
|---|---|
| `0.1.x` preview | Best effort |
| Unreleased development snapshots | No guarantee |

There is no response-time or remediation SLA during the preview.

## Reporting a vulnerability

Do not open a public issue containing exploit details, credentials, registry
tokens, private artifact names, or user data.

Until a dedicated disclosure mailbox is published, use the repository's
private **Report a vulnerability** flow if it is enabled. If that flow is not
available, contact the repository owner through a private channel listed on
their GitHub profile and ask for a secure reporting channel before sending
details.

There is no public v0.1 launch planned. Before any future public announcement,
confirm that at least one monitored private reporting path above is available
and replace this paragraph when a permanent security contact is established.

A useful report includes:

- affected n0ding revision or version;
- deployment shape and relevant redacted configuration;
- reproduction steps or a minimal proof of concept;
- expected impact and any known mitigations.

The maintainer will coordinate acknowledgement, remediation, and disclosure on
a best-effort basis.

## Current security boundaries

- There is no n0ding client authentication or authorization.
- npm upstream authorization is disabled by default.
- OCI Bearer credentials are forwarded to the configured upstream. A cached OCI
  response is served only after the upstream authorizes the same request with a
  successful `HEAD` response carrying the exact cached digest. A missing or
  different digest forces a fresh upstream `GET`.
- Redirects may cross origins for registry/CDN compatibility, but client
  credentials are retained only for the exact same scheme, host, and effective
  port. Redirect URL userinfo is discarded.
- Cookies, proxy credentials, npm OTP fields, forwarded identity headers, and
  Docker registry-auth transport headers are not forwarded upstream.
- Private/no-store/no-cache, authentication-metadata, and unsupported-`Vary`
  responses are not persisted. Cookie-bearing responses require explicit
  `Cache-Control: public`; cookie fields and other known sensitive headers are
  scrubbed again at the storage boundary.
- Status and explicit upstream/error URL log fields remove userinfo, query, and
  fragment. Secrets placed in config still exist in the file and process
  memory; arbitrary paths and error strings are not a supported secret store.
- Automated two-identity fixtures and credential canaries cover cache files,
  proxy failure logs, status output, and client-visible proxy errors. Real
  private registries and token revocation are not yet validated.
- Cached content is trusted as received from the configured upstream.
- OCI manifests and blobs are SHA-256 verified before cache commit.
- There is no malware, signature, provenance, or vulnerability scanning.
- Retention is age-based and does not enforce a strict disk quota.
- Range responses are proxied but not partially cached.
- The local cache supports one n0ding process, not shared-volume writers.

Bind n0ding to a trusted interface, terminate TLS at a trusted reverse proxy,
and review [docs/operations.md](docs/operations.md) and the
[threat model](docs/threat-model.md) before deployment.
