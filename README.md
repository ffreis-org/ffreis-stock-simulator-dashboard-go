# ffreis-stock-simulator-dashboard-go

<!-- ffreis-badges:start -->
[![CI](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/FelipeFuhr/ffreis-badges/main/badges/ffreis-stock-simulator-dashboard-go/ci.json)](https://github.com/FelipeFuhr/ffreis-stock-simulator-dashboard-go/actions) [![License](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/FelipeFuhr/ffreis-badges/main/badges/ffreis-stock-simulator-dashboard-go/license.json)](https://github.com/FelipeFuhr/ffreis-stock-simulator-dashboard-go/blob/main/LICENSE)
<!-- ffreis-badges:end -->

Go dashboard service for `ffreis-stock-simulator`.

It serves a single-page web dashboard (Go HTML template + CSS + JS) and proxies requests to the simulator HTTP API.

## What it does

- Polls simulator state via:
  - `GET /readyz`
  - `GET /v1/observe`
- Lets you trigger:
  - `POST /v1/reset`
  - `POST /v1/step_many` (single action from form)
- Adds a tabbed UI:
  - `Overview`: current simulator state/action flow
  - `Simulations`: main + alternative flow management and comparison

## Simulation Tab (Main + Alternatives)

The `Simulations` tab is feature-gated and uses dashboard proxy routes:

- `GET /api/sim/capabilities`
- `GET /api/sim/flows`
- `POST /api/sim/branches`
- `POST /api/sim/flows/{id}/step_many`
- `GET /api/sim/flows/{id}/observe`
- `GET /api/sim/flows/{id}/trace`
- `DELETE /api/sim/flows/{id}`

Behavior:

- Capability probe is cached server-side (TTL configurable) and can be force-refreshed.
- When upstream branch endpoints are unsupported, simulation controls are disabled and the tab shows a non-blocking unavailable state.
- Flow list shows lineage/index metadata (`flow_id`, hash, parent, main/branch indices, status).
- Branch creation supports rollback + patch-mode decision payloads with canonical target main-index preview.
- Comparison mode supports multiple alternatives against one canonical main baseline.
- Polling updates status/flow snapshots, while branch create/step/delete/trace operations remain explicit button actions.

Notes on semantics:

- Dashboard treats branching as main-index anchored.
- Nested branch requests are passed through; simulator owns lifecycle/persistence semantics.
- Dashboard only visualizes current upstream state.

## Config

Environment variables:

- `SIMULATOR_BASE_URL` (default: `http://localhost:8000`)
- `DASHBOARD_PORT` (default: `8080`)
- `DASHBOARD_POLL_MS` (default: `2000`)
- `SIM_CAPABILITIES_TTL` (duration, default: `5s`)
- `SWAGGER_ENABLED` (default: `false`)
- `SWAGGER_TOKEN` (optional shared token via `X-Swagger-Token` or `Authorization: Bearer`)
- `LOG_LEVEL` (`debug|info|warn|error`, default: `info`)
- `LOG_FORMAT` (`text|json`, default: `text`)
- `LOG_SOURCE` (`true|false`, default: `false`)
- `METRICS_ENABLED` (`true|false`, default: `false`)
- `DEBUG_PPROF_ENABLED` (`true|false`, default: `false`)
- `HTTP_READ_TIMEOUT` (duration, default: `10s`)
- `HTTP_WRITE_TIMEOUT` (duration, default: `15s`)
- `HTTP_IDLE_TIMEOUT` (duration, default: `60s`)
- `HTTP_READ_HEADER_TIMEOUT` (duration, default: `5s`)
- `HTTP_SHUTDOWN_TIMEOUT` (duration, default: `10s`)
- `HTTP_MAX_HEADER_BYTES` (int, default: `1048576`)
- `REQUEST_BODY_MAX_BYTES` (int, default: `1048576`)
- `UPSTREAM_TIMEOUT` (duration, default: `8s`)
- `UPSTREAM_RETRY_MAX_ATTEMPTS` (int, default: `3`)
- `UPSTREAM_RETRY_BASE_DELAY` (duration, default: `100ms`)
- `UPSTREAM_RETRY_MAX_DELAY` (duration, default: `1s`)

## Run

```bash
cd ffreis-stock-simulator-dashboard-go
go run ./cmd/dashboard
```

With readable local logs:

```bash
LOG_LEVEL=debug LOG_FORMAT=text go run ./cmd/dashboard
```

With structured JSON logs (CI/prod-friendly):

```bash
LOG_LEVEL=info LOG_FORMAT=json go run ./cmd/dashboard
```

Enable metrics and pprof for debugging:

```bash
METRICS_ENABLED=true DEBUG_PPROF_ENABLED=true go run ./cmd/dashboard
```

Then access:

- `http://localhost:8080`
- `http://localhost:8080/metrics` (when enabled)
- `http://localhost:8080/debug/pprof/` (when enabled)

If enabled, Swagger UI:

- `http://localhost:8080/swagger`

## Validation and tests

Validation path includes route/feature contracts for simulation APIs and OpenAPI drift checks via `go test`.

```bash
make test
# or
go test ./...
```

The test suite includes:

- backend `httptest` coverage for each `/api/sim/*` route (success, invalid payload, upstream 4xx/5xx, retries, capability fallback)
- route matrix contract checks for dashboard surface changes
- capability-gating checks in aggregated `/api/state`
- deterministic proxy parity checks for create/step/observe/trace/delete mappings
- lightweight frontend contract checks for tab controls, patch-mode payload shape, multi-alt comparison wiring, delete confirmation, and disabled-state behavior

## Docker

Build dashboard image:

```bash
cd ffreis-stock-simulator-dashboard-go
docker build -t ffreis-stock-dashboard:local .
```

Run dashboard container against an existing simulator:

```bash
docker run --rm -p 18080:8080 \
  -e SIMULATOR_BASE_URL=http://host.docker.internal:8000 \
  -e SWAGGER_ENABLED=true \
  ffreis-stock-dashboard:local
```

Token-protected Swagger example:

```bash
docker run --rm -p 18080:8080 \
  -e SIMULATOR_BASE_URL=http://host.docker.internal:8000 \
  -e SWAGGER_ENABLED=true \
  -e SWAGGER_TOKEN=replace-with-secret \
  ffreis-stock-dashboard:local
```

Then access:

- `http://localhost:18080/swagger`
  - add header `X-Swagger-Token: replace-with-secret` (or bearer token)

## Docker Compose (Simulator + Dashboard)

The included `docker-compose.yml` starts both services:

- `simulator` (from `../ffreis-stock-simulator/container/Containerfile`)
- `dashboard` (this Go app)

```bash
cd ffreis-stock-simulator-dashboard-go
docker compose up --build
```

Endpoints:

- Dashboard: `http://localhost:18080`
- Simulator API: `http://localhost:18000`
