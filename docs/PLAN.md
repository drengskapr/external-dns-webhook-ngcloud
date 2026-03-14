# Implementation Plan: external-dns webhook for ngcloud.ru

## Overview

A Go HTTP server implementing the [external-dns webhook provider spec](https://kubernetes-sigs.github.io/external-dns/v0.14.2/tutorials/webhook-provider/). It translates external-dns `Endpoint` objects into ngcloud deck-api calls (the same multi-step flow as `dns_record_create.sh`).

---

## Project structure

```
.
├── main.go
├── go.mod / go.sum
├── internal/
│   ├── ngcloud/
│   │   ├── client.go        # HTTP client, auth, all API calls
│   │   ├── operations.go    # create / delete / modify flows
│   │   └── types.go         # request/response structs for deck-api
│   └── webhook/
│       ├── server.go        # HTTP server, routes
│       ├── handlers.go      # GET /records, POST /records, GET /, POST /adjustendpoints
│       └── types.go         # Endpoint, Changes (matching external-dns JSON schema)
├── Dockerfile
└── deploy/
    └── helm/ or kustomize/  # K8s manifests
```

---

## Webhook API contract

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Returns `DomainFilter` JSON; used for negotiation |
| `GET` | `/healthz` | Health check → 200 OK |
| `GET` | `/records` | Returns `[]*Endpoint` of all current DNS records |
| `POST` | `/records` | Receives `Changes{Create, UpdateOld, UpdateNew, Delete}`, applies them |
| `POST` | `/adjustendpoints` | Receives `[]*Endpoint`, returns them (passthrough or adjusted) |

Content-Type: `application/external.dns.webhook+json;version=1`

---

## ngcloud client — key operations

**Listing records** (`GET /records`):
1. `GET /instances?serviceId=111&pageSize=1000` → list all DNS record instances
2. For each instance, fetch its CFS params to reconstruct `Endpoint` fields (zone, name, type, value, TTL)
3. Return assembled `[]Endpoint`

**Creating a record** (`POST /records` → `Changes.Create`): mirrors the shell script exactly:
1. Fetch CFS param definitions for operation 45 (create)
2. `POST /instances` → create instance with `displayName = "dnsrecord-<name>"`
3. `GET /instances?serviceId=111` → find instanceUID by displayName
4. `POST /instanceOperations` → create operation, extract operationUID from `Location` header
5. `POST /instanceOperationCfsParams` × N → push each CFS param
6. `POST /instanceOperations/{uid}/run` → start the operation
7. Poll `GET /instanceOperations/{uid}` until `isSuccessful=true` or error

**Deleting a record** (`Changes.Delete`): operation ID 46 (no CFS params needed — just create op + run)

**Updating a record** (`Changes.UpdateOld/UpdateNew`): delete old instance, create new one (ngcloud has no atomic update; modify operation 90 can be used if CFS structure is similar, but delete+create is safer for now)

---

## Zone mapping

ngcloud requires a `zone UUID` for every record, but external-dns works with zone names (e.g. `example.com`). Solution: a configurable map `NGCLOUD_ZONE_MAP=example.com=<uuid>,foo.bar=<uuid>` passed via env var. The webhook resolves the zone UUID at record creation time by matching the longest suffix of `DNSName`.

---

## Configuration (env vars)

| Variable | Default | Description |
|----------|---------|-------------|
| `NGCLOUD_TOKEN` | — | Bearer token (required) |
| `NGCLOUD_BASE_URL` | `https://deck-api.ngcloud.ru/api/v1/index.cfm` | deck-api base URL |
| `NGCLOUD_ZONE_MAP` | — | Comma-separated `zone=uuid` pairs |
| `NGCLOUD_SERVICE_ID` | `111` | DNS Records service ID |
| `NGCLOUD_OP_CREATE` | `45` | Create operation ID |
| `NGCLOUD_OP_DELETE` | `46` | Delete operation ID |
| `NGCLOUD_OP_MODIFY` | `90` | Modify operation ID |
| `DOMAIN_FILTER` | `""` | Comma-separated domain filter for external-dns |
| `SERVER_PORT` | `8888` | Webhook HTTP port |
| `POLL_MAX_ATTEMPTS` | `60` | Operation polling max attempts |
| `POLL_INTERVAL` | `5s` | Sleep between poll attempts |

---

## Implementation steps (ordered)

1. `go mod init` + add dependencies (`sigs.k8s.io/external-dns` for types, standard `net/http`)
2. `internal/ngcloud/types.go` — deck-api request/response structs
3. `internal/ngcloud/client.go` — HTTP client with auth, low-level methods
4. `internal/ngcloud/operations.go` — high-level `CreateRecord`, `DeleteRecord`, `ListRecords`
5. `internal/webhook/types.go` — `Endpoint`, `Changes`, `DomainFilter` (matching external-dns JSON)
6. `internal/webhook/handlers.go` — implement 5 route handlers
7. `internal/webhook/server.go` — wire up routes, middleware (content-type check)
8. `main.go` — parse config, wire components, start server
9. `Dockerfile` — multi-stage Go build
10. K8s `Deployment` + `Service` manifests (sidecar pattern next to external-dns pod)

---

## Key design decisions

- **No external framework**: use only `net/http` + `encoding/json` to keep dependencies minimal
- **Polling is synchronous within the request**: the `POST /records` handler blocks until all ngcloud operations complete (or timeout). This is safe because external-dns expects synchronous completion.
- **CFS param IDs are fetched once at startup** and cached, not on every record operation
- **Instance identity**: ngcloud instances are identified by `displayName = "dnsrecord-<recordName>"`. For records with multiple targets, each target gets its own instance with a suffix (e.g. `dnsrecord-foo.example.com-0`).
