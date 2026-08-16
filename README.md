# CliSynth

CliSynth is the synthetic CLI engine in the OpenNV ecosystem. It does the
inverse of TextFSM: an OutputFSM file starts with literal command output, marks
the values that should come from inventory, and optionally derives values with
deterministic generators, conditions, and sandboxed Starlark.

It lets automation developers exercise parsers, validators, APIs, and user
workflows against an inventory-shaped network that does not exist. CliSynth
does **not** connect to network devices and is not a device simulator or a
replacement for integration testing against vendor software.

## What is included

- Strict, versioned YAML OutputFSM definitions
- Native support for the OpenNV `outputfsm-packs` catalog format
- Exact platform and command resolution with explicit aliases
- Literal `{{ variable }}` substitution (no hidden template execution)
- Nested inventory-field lookup and defaults
- Deterministic `integer`, `choice`, `hex`, `mac`, and documentation-range
  `ipv4` generators
- Starlark boolean conditions and an optional `process(device, vars, seed)`
  function
- Precompiled Starlark, execution-step limits, no `load`, no host functions,
  and no top-level execution
- Ordered batch rendering with bounded concurrency and per-device errors
- A Go library, CLI, and versioned HTTP API
- Checked-in [OpenAPI](api/openapi.yaml) and [OutputFSM JSON Schema](schema/outputfsm.schema.json) contracts
- Health/readiness endpoints, strict JSON, request IDs, structured logs, CORS
  allowlisting, body limits, and graceful shutdown
- Bearer-authenticated, count/digest-verified atomic GitOps pack reloads
- A source-agnostic inventory adapter interface for later MongoDB, GraphQL,
  Nautobot, and NetBox integrations
- Unit, HTTP integration, CLI integration, race, and benchmark tests
- A non-root distroless container image

## Quick start

Requirements: Go 1.25 or a Go installation with automatic toolchain download.

```bash
go test ./...
go run ./cmd/clisynth validate --definitions ./examples/definitions
go run ./cmd/clisynth render \
  --definitions ./examples/definitions \
  --inventory ./examples/inventory/router01.json \
  --command "show version" \
  --seed 42
```

Run the API:

```bash
go run ./cmd/clisynth serve \
  --definitions ./examples/definitions \
  --addr :8080
```

Then render a device:

```bash
curl --fail-with-body http://localhost:8080/v1/render \
  --header 'Content-Type: application/json' \
  --data '{
    "command": "show version",
    "seed": 42,
    "device": {
      "id": "device-0001",
      "name": "edge-rtr-01",
      "platform": "cisco_ios",
      "software_version": "15.9(3)M8",
      "status": "active",
      "custom_fields": {"uptime_days": 92}
    }
  }'
```

## OutputFSM format

The format is deliberately narrow. A definition owns one canonical platform
and command plus explicit aliases. Template tokens are plain identifiers, and
every token must be supplied by a variable or condition assignment.

CliSynth accepts two explicit dialects:

- `opennv.io/v1alpha1`, the shared OpenNV pack format used by the sibling
  `outputfsm-packs` repository (`${variable}` templates, fixture defaults,
  normalized results, and allow-listed pure generators); and
- `clisynth.opennv.io/v1alpha1`, the native extended format shown below, which
  adds seeded generators, conditions, and sandboxed Starlark.

When pointed at an OpenNV pack root, discovery is limited to
`packs/*/commands/*.yaml`; catalog and per-platform index files are not treated
as definitions. The first reviewed synthetic fixture supplies a complete
baseline, and fields provided by the request override that baseline.

```bash
OUTPUTFSM_ROOT=../outputfsm-packs go run ./cmd/clisynth validate
# validated 160 OutputFSM definitions from ../outputfsm-packs
```

```yaml
apiVersion: clisynth.opennv.io/v1alpha1
kind: OutputFSM
metadata:
  name: cisco-ios-show-version
  version: 1.0.0
  description: Cisco IOS show version output for development.
  labels:
    vendor: cisco
spec:
  platform: cisco_ios
  platformAliases: [ios]
  command: show version
  commandAliases: [show ver]

  variables:
    hostname:
      source: device.name
      required: true
      transform: upper
    software_version:
      source: device.software_version
      default: 15.9(3)M8
    serial:
      generator:
        kind: hex
        prefix: FTX
        length: 8
    operational_state:
      default: unknown

  conditions:
    - when: device.get("status", "active") == "active"
      set:
        operational_state: up
      else:
        operational_state: down

  starlark: |
    def process(device, vars, seed):
        vars["label"] = vars["hostname"] + ":" + str(seed)
        return vars

  template: |
    {{ hostname }} uptime is 31 days
    Version {{ software_version }}
    Processor board ID {{ serial }}
    Operational state is {{ operational_state }}
```

### Variables

Each variable must choose one source:

- `source`: a dotted inventory path; the leading `device.` is optional.
- `generator`: a deterministic generator definition.
- `default`: a literal value. A source may also have a fallback default.

`required: true` fails the render if a source is missing and has no default.
Supported transforms are `string`, `lower`, `upper`, `trim`, and `json`.
Array indexes are supported in paths, for example
`device.interfaces.0.address`.

### Deterministic generators

Generator output is derived from the request seed, stable device identity,
definition name, and variable name. The same inputs always produce the same
output regardless of batch concurrency or process scheduling. Identity uses
the first available field in `id`, `name`, `hostname`, `primary_ip.address`,
and `address`, falling back to the canonical document.

```yaml
serial:
  generator: {kind: hex, prefix: FTX, length: 8}
vlan:
  generator: {kind: integer, min: 2, max: 4094}
state:
  generator: {kind: choice, values: [up, down]}
mac:
  generator: {kind: mac, prefix: "02:42"}
test_address:
  generator: {kind: ipv4}
```

The IPv4 generator intentionally emits only RFC 5737 `198.51.100.0/24`
documentation addresses.

### Conditions and Starlark

Conditions are Starlark expressions with only `device`, `vars`, and `seed` in
scope. An assignment is literal unless it uses a field reference:

```yaml
conditions:
  - when: device.get("site", {}).get("name", "") == "dfw01"
    set:
      region: central
      contact: {from: device.custom_fields.owner, default: noc}
    else:
      region: other
```

The optional processor must define exactly
`process(device, vars, seed)` and return a string-keyed dictionary. Helper
functions are allowed. CliSynth compiles scripts when definitions load and
rejects syntax errors, imports, top-level execution, and a wrong signature.
At render time it exposes no filesystem, network, environment, clock, random,
or host-language functions. Each invocation has a configurable execution-step
limit and respects request cancellation.

This is a strong application sandbox for the exposed Starlark surface, but it
does not make unreviewed definitions trustworthy configuration. Review and
version definitions like code.

## HTTP API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Process liveness |
| `GET` | `/readyz` | Readiness and loaded definition count |
| `GET` | `/v1/definitions` | Sorted definition catalog |
| `POST` | `/v1/render` | Render one device and command |
| `POST` | `/v1/render:batch` | Render an ordered device/command batch |
| `POST` | `/v1/execution/render` | OpenNV execution-backend compatibility batch |
| `PUT` | `/v1/admin/definitions` | Authenticated atomic replacement of the complete definition snapshot |

Batch request:

```json
{
  "concurrency": 32,
  "requests": [
    {
      "command": "show version",
      "seed": 42,
      "device": {"id": "1", "name": "r1", "platform": "cisco_ios"}
    }
  ]
}
```

The service caps concurrency at the process-level maximum. Results retain
input order and include their original zero-based index. A bad device produces
a coded error in its batch item without failing successful peers. A malformed
top-level batch is rejected before work starts. The native hard request limit
is 1,000,000 items.

The compatibility endpoint accepts the shared `opennv-contracts` execution
shape (`apiVersion: v1`) and fails closed unless `mode` is `emulated`. It fans
out devices in input order and commands in input order, generates stable task
IDs from `runId + deviceId + command`, and returns TaskResult-shaped evidence
with `backend: clisynth` and `synthetic: true`. Canonical camel-case inventory
fields are mapped into the nested pack inventory, while `customFields` can
override any fixture subtree.

With the service running against `outputfsm-packs`, the checked-in request can
be executed directly:

```bash
curl --fail-with-body http://localhost:8080/v1/execution/render \
  --header 'Content-Type: application/json' \
  --data @examples/execution-request.json
```

```json
{
  "apiVersion": "v1",
  "requestId": "req-1",
  "runId": "run-1",
  "mode": "emulated",
  "inventoryRevision": "inventory-sha",
  "packRevision": "pack-sha",
  "devices": [{
    "id": "device-1",
    "name": "edge-rtr-01",
    "platform": "cisco_ios",
    "managementAddress": "192.0.2.10",
    "role": "edge",
    "site": "dfw01",
    "status": "active",
    "osVersion": "15.9(3)M8"
  }],
  "commands": ["show version"],
  "concurrency": 64
}
```

Error envelope:

```json
{
  "error": {
    "code": "definition_not_found",
    "message": "no OutputFSM definition for platform ..."
  }
}
```

Stable codes are `invalid_request`, `definition_not_found`,
`inventory_field_error`, `processing_error`, and `canceled`.

### Atomic GitOps pack reload

Set `CLISYNTH_RELOAD_TOKEN` to a randomly generated bearer token of at least 32
visible ASCII bytes to enable `PUT /v1/admin/definitions`. When the variable is
unset, the administrative route is not registered. The token is dedicated to
this endpoint; do not reuse a user, GitHub, or device credential. For example:

```bash
export CLISYNTH_RELOAD_TOKEN="$(openssl rand -hex 32)"
```

The trusted OpenNV backend sends a **complete** pack snapshot. Deltas are not
accepted. Each `yaml` value is the unmodified UTF-8 content of one `.yaml` or
`.yml` definition from the repository, and `source` is its clean, relative
POSIX path:

```json
{
  "apiVersion": "clisynth.opennv.io/v1alpha1",
  "kind": "OutputFSMBundle",
  "revision": "f2c47bd88d7a04a13f95c7dc2a458f5192e2f301",
  "complete": true,
  "expectedCount": 160,
  "digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "definitions": [
    {
      "source": "packs/cisco_ios/commands/show_version.yaml",
      "yaml": "apiVersion: opennv.io/v1alpha1\nkind: OutputFSM\n..."
    }
  ]
}
```

The digest is lowercase SHA-256 over the definitions sorted lexically by
`source`. For each definition, hash an unsigned 64-bit big-endian byte length,
then the UTF-8 source bytes, then an unsigned 64-bit big-endian byte length,
then the exact YAML bytes. `expectedCount`, `complete`, and the digest protect
against accidentally applying a truncated or delta payload.

CliSynth authenticates before reading the body, enforces overall, count, path,
and per-document limits, strictly decodes every YAML document, and compiles the
entire candidate registry including Starlark and key conflicts. Only after all
checks pass does it publish the new immutable registry snapshot. Failed
requests leave the active snapshot unchanged, while in-flight renders see
either the old or the new complete snapshot.

A successful response is:

```json
{
  "apiVersion": "clisynth.opennv.io/v1alpha1",
  "revision": "f2c47bd88d7a04a13f95c7dc2a458f5192e2f301",
  "count": 160,
  "digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

Send `Authorization: Bearer $CLISYNTH_RELOAD_TOKEN` and
`Content-Type: application/json`. The endpoint returns `401` for missing or
invalid authentication, `413` for an oversized body, `415` for a non-JSON
content type, and `400`/`422` for malformed or invalid bundles. Responses use
`Cache-Control: no-store`.

## CLI

```text
clisynth serve [flags]
clisynth validate [flags]
clisynth list [--json] [flags]
clisynth render --inventory FILE --command COMMAND [--json] [flags]
clisynth render-batch --input FILE [flags]
clisynth version
```

`examples/inventory/batch.json` is a complete batch request.

Server environment variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `OUTPUTFSM_ROOT` | `./examples/definitions` | OpenNV pack root, native definition directory, or one file |
| `CLISYNTH_DEFINITIONS` | empty | Legacy fallback when `OUTPUTFSM_ROOT` is unset |
| `PORT` | `8080` | Hosting-platform listen port |
| `CLISYNTH_ADDR` | empty | Full listen-address override, such as `127.0.0.1:8080` |
| `CLISYNTH_MAX_CONCURRENCY` | `64` | Process batch worker cap |
| `CLISYNTH_CORS_ORIGINS` | empty | Exact comma-separated browser origins |
| `CLISYNTH_RELOAD_TOKEN` | empty | Dedicated 32+ byte bearer token; leaving it empty disables the admin route |
| `CLISYNTH_RELOAD_MAX_BODY_BYTES` | `33554432` | Maximum JSON reload request size (hard ceiling `268435456`) |
| `CLISYNTH_RELOAD_MAX_DEFINITIONS` | `10000` | Maximum definitions in one complete snapshot |

## Library use

```go
definitions, err := outputfsm.LoadDir("./definitions")
registry, err := engine.NewRegistry(definitions)
renderer, err := engine.New(registry, engine.Config{MaxConcurrency: 64})

result, err := renderer.Render(ctx, engine.RenderRequest{
    Command: "show version",
    Device: map[string]any{
        "id": "device-1", "name": "r1", "platform": "cisco_ios",
    },
    Seed: 42,
})
```

`inventory.Source` is the adapter boundary for future inventory backends. An
adapter returns plain `inventory.Document` values; no MongoDB, GraphQL,
Nautobot, or NetBox types leak into the render engine. This keeps the core
deterministic and easy to test. The current API accepts an inline inventory
document so the demo is self-contained.

## Container

```bash
docker build -t clisynth:dev .
docker run --rm -p 8080:8080 clisynth:dev
```

The final image is distroless, runs as a non-root user, and contains only the
binary and example definitions. Mount a reviewed pack at `/app/definitions`
for real use.

## Development

```bash
make check
go test -race ./...
go test -bench=. -benchmem ./pkg/engine
go test -run '^$' -bench '^BenchmarkRenderBatch100000$' -benchmem -benchtime=1x ./pkg/engine
```

Definition loading is transactional: all files and all platform/command keys
must validate before a new registry snapshot becomes visible. YAML decoding
rejects unknown keys and multiple documents. Files are loaded in lexical order
and directory discovery ignores or rejects symlinked definitions and pack-root
escapes.

## Project status

CliSynth is pre-1.0. The `v1alpha1` OutputFSM API can evolve before the first
stable release. Pin definition-pack and binary versions together in demos and
CI. Synthetic output in this repository is illustrative and is not supplied,
endorsed, or certified by any network vendor.

## License

Apache-2.0. See [LICENSE](LICENSE).
