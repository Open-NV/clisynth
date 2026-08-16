# Security policy

## Reporting

Please report suspected vulnerabilities privately to the OpenNV maintainers
through GitHub's private vulnerability reporting feature. Do not include real
credentials, production inventory, or customer command output in a report.

## Security model

CliSynth treats OutputFSM files as reviewed configuration. Starlark has no
imports, host functions, filesystem, network, process, environment, clock, or
random access. Only function definitions are allowed at script top level, and
execution is step-limited and cancelable. HTTP bodies, batch sizes, and worker
concurrency are bounded.

The render, catalog, health, and readiness endpoints do not provide user
authentication or authorization. Deploy them behind an authenticated gateway
and a network policy when the service is reachable outside a developer
workstation. Configure an explicit CORS allowlist; CORS is not an
authorization control.

The optional atomic definition-reload endpoint is separately protected by
`CLISYNTH_RELOAD_TOKEN`. It is not registered when the token is unset, rejects
tokens shorter than 32 bytes, compares credentials in constant time, and
authenticates before reading a request body. Generate a unique high-entropy
token, deliver it through the deployment platform's secret store, rotate it as
a service credential, and allow network access only from the trusted OpenNV
backend. Do not expose this endpoint merely because it has a bearer token.

Reload accepts only complete, count- and digest-verified snapshots. It bounds
the JSON body, document count, and each YAML document; rejects unknown fields,
duplicate sources, path traversal, multi-document YAML, duplicate registry
keys, and failed Starlark compilation; and does not publish a candidate until
all definitions compile. A rejected bundle leaves the current snapshot intact.

Never mount production device credentials into the container. When reload is
enabled, inject only its narrowly scoped service token through the deployment
secret store. Inventory values may be echoed into rendered output and API
responses by design.
