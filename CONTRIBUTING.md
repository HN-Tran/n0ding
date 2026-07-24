# Contributing

n0ding is accepting focused fixes and evidence for its narrow, read-only npm +
OCI preview. Please open an issue before investing in a large change.

## Scope

Good v0.1.x contributions include:

- correctness, compatibility, security, and reliability fixes;
- tests for existing npm or OCI pull behavior;
- operating and troubleshooting documentation;
- reproducible client compatibility results.

UI work, publishing, user management, RBAC, PyPI, new registry ecosystems,
database storage, and broad dependency additions are out of scope until the
documented release gates are revisited.

Security vulnerabilities must follow [SECURITY.md](SECURITY.md), not the public
issue tracker.

## Development workflow

Use Go 1.24 or newer within the Go 1 compatibility promise:

```sh
go test -race ./...
go vet ./...
go build -trimpath -o dist/n0ding ./cmd/n0ding
docker compose config --quiet
docker build --tag n0ding:contributor .
```

When runtime behavior changes:

1. Add or update an automated test.
2. Keep the default build dependency-light and justify every new dependency.
3. Update `docs/compatibility.md` when client behavior or support changes.
4. Update configuration, operations, troubleshooting, and changelog text where
   relevant.
5. Keep credentials, registry tokens, cache data, and generated binaries out of
   commits.

Keep commits focused and explain the observable behavior in the pull request.
Contributions are submitted under the repository's
[Apache-2.0 license](LICENSE).
