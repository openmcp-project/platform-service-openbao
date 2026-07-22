# platform-service-openbao end-to-end tests

Runs the compiled manager image against a real Kubernetes cluster (via
[Kind](https://kind.sigs.k8s.io/)) and a real OpenBao/Vault backend
(via [testcontainers-go](https://golang.testcontainers.org/)). The
whole environment is created and torn down within a single `go test`
invocation — no persistent local state.

## Prerequisites

- Go 1.26+
- Docker (for Kind and testcontainers)
- `make`
- Sufficient GHCR access if pulling `ghcr.io/openmcp-project/images/...`
  (the openmcp-operator + kind cluster-provider images). Public images
  need no credentials.

## Running

The Makefile target owns the flow:

```
make test-e2e
```

Under the hood this runs:

```
go test -tags=e2e ./test/e2e/... -count=1 -timeout=30m -v
```

Expect a first-run wall clock of 5-10 minutes (image pulls dominate).
Subsequent runs on a warm cache take 2-3 minutes.

## Environment variables

| Variable            | Default                    | Purpose                                                                                                                                            |
| ------------------- | -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `IMG`               | (built by `TestMain`)      | Pre-built manager image tag. When unset, `TestMain` runs `make docker-build IMG=platform-service-openbao:e2e` before Kind starts.                  |
| `E2E_BACKEND_IMAGE` | `openbao/openbao:latest`   | Backend image. Any OpenBao or Vault image works; the wrapper detects which entrypoint (`bao server -dev` vs `vault server -dev`) by image path.    |

## Running against HashiCorp Vault

```
E2E_BACKEND_IMAGE=hashicorp/vault:latest make test-e2e
```

The suite is backend-agnostic: same test code, same assertions, one
env var. Vault is **not** a supported target per the openspec, but
this switch keeps the compatibility claim honest.

## Architecture

```
                ┌────────────────────────────────────────────┐
                │        Docker network (dynamic name)       │
                │                                            │
                │  ┌──────────────────┐   ┌───────────────┐  │
                │  │ Kind cluster     │   │ OpenBao / Vault│  │
                │  │ (single-node)    │   │ dev-server    │  │
                │  │                  │   │ alias:openbao │  │
                │  │  openmcp-operator│   │ :8200 (HTTP)  │  │
                │  │  + PlatformSvc   │   │               │  │
                │  │  (this project)  │   │               │  │
                │  └──────┬───────────┘   └──────┬────────┘  │
                │         │                     │           │
                │         │  http://openbao:8200│           │
                │         └─────────────────────┘           │
                └────────────────────────────────────────────┘
                              ▲
                              │ testcontainers publishes
                              │ backend port to host:<random>
                              │
                              └── Host-side tests
                                  (seed policy, read role)
```

Kind and the backend container share a Docker network so the manager
pod can reach the backend by DNS alias (`http://openbao:8200`). The
network is created by testcontainers-go and passed to Kind via the
`KIND_EXPERIMENTAL_DOCKER_NETWORK` environment variable in `TestMain`.

## What the suite covers today

`TestPolicyBinding_TrustIntegration`:

1. `OpenBaoInstance` reaches `OpenBaoReachable=True` (reconciler
   successfully probes the running backend).
2. `PolicyBinding` publishes a deterministic `status.roleName` and
   `status.policyExists=True` after a matching policy is seeded on the
   backend.
3. The OpenBao JWT role actually exists on the backend with exactly
   the named policy attached (spec Requirement 5).
4. No Kubernetes `Secret` in the tenant namespace holds an OpenBao
   token (spec Requirement 7 invariant).

## What is NOT covered yet

- Full JWT ServiceAccount login flow — depends on `AccessRequest`
  wiring to target ControlPlanes (openspec tasks 2.5, 4.4). The
  reconcilers currently report `ControlPlaneUnavailable` for that
  path.
- `ControlPlaneTrust` full readiness — issuer/JWKS discovery depends
  on target ControlPlane API access (openspec task 4.3).
- `ProjectEntity` OpenBao identity materialisation (open question in
  design.md).

The `20-controlplanetrust.yaml` and `30-controlplaneentity.yaml`
fixtures are applied so the reconcilers exercise the "dependency not
ready" path; both intentionally report `Ready=False`.

## Troubleshooting

- **`no such image: platform-service-openbao:e2e`**: `make docker-build`
  failed before the test binary ran Kind's image load. Look above the
  Kind messages in the test output.
- **Backend never becomes reachable**: `docker ps` and check the
  `openbao/openbao:latest` container is running on the ephemeral Kind
  network. `docker logs <container>` shows dev-server startup errors.
- **Kind cluster leaks after a Ctrl-C**: `kind delete clusters --all`.
  The suite normally cleans up via `envfuncs.DestroyCluster`, but a
  hard interrupt bypasses that path.

## CI status

The GitHub Actions workflow (`.github/workflows/test-e2e.yml`) is
currently `workflow_dispatch` only. Re-enable `push` / `pull_request`
triggers once the suite has passed at least once on `ubuntu-latest`.
GHA runners have Docker natively, so no extra setup is required.

## Known environment limitations

Kind creates its cluster as a Docker container that runs systemd
inside itself. This requires cgroup delegation and PID namespace
handling that some Docker environments don't expose:

- ✅ **GitHub Actions `ubuntu-latest`** — works.
- ✅ **Native Linux Docker** — works.
- ⚠️ **Docker Desktop for macOS / Windows** — works with recent
  versions.
- ❌ **Docker-in-container** (Lima, some cloud VMs) — Kind fails with
  `could not find a log line that matches "Reached target .* Multi-User
  System.*"`. Nothing about the test suite is wrong; the constrained
  Docker daemon simply can't run systemd inside a nested container.

If you hit the failure above locally, run the suite in CI or on a
native Linux host. The `backend` subpackage runs everywhere and is a
useful sanity check when troubleshooting:

```
go test -tags=e2e ./test/e2e/backend/... -count=1
```
