# Implementation Plan: external-dns webhook for ngcloud.ru

## Overview

A Go HTTP server implementing the [external-dns webhook provider spec](https://kubernetes-sigs.github.io/external-dns/v0.14.2/tutorials/webhook-provider/). It translates external-dns `Endpoint` objects into ngcloud deck-api calls (the same multi-step flow as `dns_record_create.sh`).

Reference implementation: [cert-manager-webhook-ngcloud](https://github.com/drengskapr/cert-manager-webhook-ngcloud) — a working ngcloud API client in the same language. Reuse patterns and code from `ngcloud/client.go` there.

---

## Project structure

```
.
├── main.go
├── go.mod / go.sum
├── Dockerfile
├── Makefile
├── ngcloud/
│   └── client.go        # HTTP client, auth, all deck-api calls (modelled after cert-manager webhook)
├── webhook/
│   ├── server.go        # HTTP server, routes, middleware
│   ├── handlers.go      # GET /, GET /healthz, GET /records, POST /records, POST /adjustendpoints
│   └── types.go         # Endpoint, Changes, DomainFilter (matching external-dns JSON schema)
└── deploy/
    └── external-dns-webhook-ngcloud/   # Helm chart
        ├── Chart.yaml
        ├── values.yaml
        └── templates/
```

> Flat `ngcloud/` package (not `internal/`) mirrors the cert-manager webhook structure.

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

## ngcloud deck-api — reference

**Base URL:** `https://deck-api.ngcloud.ru/api/v1/index.cfm`
**Auth:** `Authorization: Bearer <token>` on every request
**HTTP client timeout:** 30 seconds
**Service ID (DNS Records):** `111`
**Operation IDs:** create=`45`, delete=`46`, modify=`90`
**Default TTL:** 120 seconds

### CFS parameter fetch

```
GET /instanceOperations/default/{svcOperationId}?fields=operation,svcOperationId,cfsParams
```

Returns the CFS param definitions (label → svcOperationCfsParamId). Labels are Russian strings:
- `"UUID Зоны"` — zone UUID
- `"Тип DNS-записи"` — record type (A, CNAME, TXT, …)
- `"Имя записи"` — record name
- `"Значение записи"` — record value / IP
- `"TTL записи (в секундах)"` — TTL

### Create record flow (confirmed by both the shell script and cert-manager webhook)

1. `GET /instanceOperations/default/45?fields=...` → fetch CFS param label→ID map (cache at startup)
2. `POST /instances` `{serviceId: 111, displayName: "dnsrecord-<name>", descr: ""}` → create instance
3. `GET /instances?fields=instanceUid,displayName,instanceConfigDtCreated&serviceId=111&page=1&pageSize=100` → find instanceUID by displayName
4. `POST /instanceOperations` `{svcOperationId: 45, instanceUid: "<uid>"}` → create operation; extract operationUID from `Location` response header
5. `POST /instanceOperationCfsParams` `{paramValue, instanceOperationUid, svcOperationCfsParamId}` — one request per CFS param
6. `POST /instanceOperations/{operationUID}/run` → start the operation
7. Poll `GET /instanceOperations/{operationUID}` every 5 s (max 60 attempts) until `dtFinish` is non-empty; check `isSuccessful`

### Delete record flow

1. Find instanceUID by displayName via `GET /instances?serviceId=111`
2. `POST /instanceOperations` `{svcOperationId: 46, instanceUid: "<uid>"}` → create delete operation
3. `POST /instanceOperations/{operationUID}/run`
4. Poll until completion (same as create). Success also indicated by response containing `"Услуга удалена"`.
No CFS params needed for delete.

### Poll completion logic

```
completed = dtFinish != "" && dtFinish != null
success   = isSuccessful == true
```

Poll until `completed`. Then check `success`; if false, read `errorLog`.

---

## ngcloud client — key operations

**Listing records** (`GET /records`):
1. `GET /instances?serviceId=111&pageSize=1000` → list all DNS record instances
2. For each instance, fetch its active CFS param values to reconstruct `Endpoint` fields (name, type, value, TTL)
3. Return assembled `[]Endpoint`

**Creating a record** (`Changes.Create`): see create flow above.

**Deleting a record** (`Changes.Delete`): see delete flow above.

**Updating a record** (`Changes.UpdateOld/UpdateNew`): delete old instance, then create new one. ngcloud has no atomic modify; delete+create is safer than operation 90 until the modify CFS structure is confirmed.

---

## Zone mapping

ngcloud requires a zone UUID for every record, but external-dns works with zone names (e.g. `example.com`). Solution: a configurable map `NGCLOUD_ZONE_MAP=example.com=<uuid>,foo.bar=<uuid>` passed via env var. The webhook resolves the zone UUID at record creation time by matching the longest suffix of `DNSName`.

---

## Configuration (env vars)

| Variable | Default | Description |
|----------|---------|-------------|
| `NGCLOUD_TOKEN` | — | Bearer token (required) |
| `NGCLOUD_BASE_URL` | `https://deck-api.ngcloud.ru/api/v1/index.cfm` | deck-api base URL |
| `NGCLOUD_ZONE_MAP` | — | Comma-separated `zone=uuid` pairs (required) |
| `NGCLOUD_SERVICE_ID` | `111` | DNS Records service ID |
| `NGCLOUD_OP_CREATE` | `45` | Create operation ID |
| `NGCLOUD_OP_DELETE` | `46` | Delete operation ID |
| `NGCLOUD_OP_MODIFY` | `90` | Modify operation ID |
| `NGCLOUD_DEFAULT_TTL` | `120` | Default TTL in seconds |
| `DOMAIN_FILTER` | `""` | Comma-separated domain filter for external-dns |
| `SERVER_PORT` | `8888` | Webhook HTTP port |
| `POLL_MAX_ATTEMPTS` | `60` | Operation polling max attempts |
| `POLL_INTERVAL` | `5s` | Sleep between poll attempts |

---

## Implementation steps (ordered)

1. `go mod init github.com/drengskapr/external-dns-webhook-ngcloud` + add `sigs.k8s.io/external-dns` for types, `k8s.io/klog/v2` for structured logging
2. `ngcloud/client.go` — HTTP client with 30 s timeout, Bearer auth, low-level `get`/`post` helpers; high-level `CreateRecord`, `DeleteRecord`, `ListRecords`; CFS param cache populated at startup
3. `webhook/types.go` — `Endpoint`, `Changes`, `DomainFilter` structs
4. `webhook/handlers.go` — implement 5 route handlers
5. `webhook/server.go` — wire routes, content-type middleware
6. `main.go` — parse config, init klog (ISO8601), wire components, start server
7. `Dockerfile` — multi-stage Go build → `gcr.io/distroless/static` (same as cert-manager webhook)
8. `Makefile` — `build`, `test`, `docker-build`, `helm-install` targets
9. Helm chart under `deploy/` — Deployment + Service + ConfigMap for zone map + Secret for token

---

## Key design decisions

- **Logging:** `k8s.io/klog/v2` with ISO8601 timestamps — consistent with cert-manager webhook
- **No external HTTP framework:** `net/http` + `encoding/json` only
- **Polling is synchronous within the request:** `POST /records` blocks until all ngcloud operations complete. External-dns expects synchronous completion.
- **CFS param IDs cached at startup:** fetched once per operation type (create/delete), not per record call
- **Instance naming:** `displayName = "dnsrecord-<recordName>"`. For multiple targets on the same name, append index suffix: `dnsrecord-foo.example.com-0`, `dnsrecord-foo.example.com-1`
- **Distroless base image:** `gcr.io/distroless/static` for minimal attack surface

---

## Testing

- DNS nameserver for test resolution: `185.247.187.83:53` (ns3.ngcloud.ru / Nubes nameserver)
- Required env vars for integration tests: `TEST_ZONE_NAME`, `TEST_ZONE_UID`, `NGCLOUD_TOKEN`
