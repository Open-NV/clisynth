# Contributing to CliSynth

Thank you for helping make network automation safer to test.

1. Open an issue for behavior or schema changes before a large implementation.
2. Keep the render core deterministic. Do not add clock, environment, network,
   filesystem, or process access to OutputFSM execution.
3. Add focused tests for every behavior change and a fixture for every new
   definition.
4. Run `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...`.
5. Do not submit copied proprietary command output, credentials, production
   inventory, customer identifiers, or vendor-confidential material.

Output definitions should use invented hostnames and RFC 5737 documentation
addresses. Describe the platform/version shape they model, and keep aliases
explicit rather than relying on fuzzy command matching.

Contributions are accepted under the Apache-2.0 license. Participation is
governed by the [OpenNV Code of Conduct](CODE_OF_CONDUCT.md).
