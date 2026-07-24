# Contributing

n0ding is currently validating a narrow technical spike. Before proposing a
large feature, check the non-goals and go/kill criteria in the decision paper.

For code changes:

1. Keep the default build dependency-light.
2. Add integration tests using standard protocol behavior.
3. Run `go test -race ./...` and `go vet ./...`.
4. Update the compatibility matrix when client behavior changes.

Protocol breadth is intentionally gated. New ecosystems should not be added
before the npm spike is evaluated.
