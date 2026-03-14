# Project reference for Claude

## What this is

An [external-dns webhook provider](https://kubernetes-sigs.github.io/external-dns/v0.14.2/tutorials/webhook-provider/) for [ngcloud.ru](https://ngcloud.ru) (Nubes cloud). It runs as a sidecar to external-dns and translates external-dns `Endpoint` objects into ngcloud deck-api calls.

## Module

`github.com/drengskapr/external-dns-webhook-ngcloud`

## Structure

```
main.go                         # config loading, wiring, startup
internal/ngcloud/
  types.go                      # deck-api request/response structs + Record type
  client.go                     # HTTP client, all deck-api logic
internal/webhook/
  types.go                      # Endpoint, Changes, DomainFilter (external-dns JSON schema)
  handlers.go                   # HTTP handler funcs
  server.go                     # ServeMux routing, contentType constant
```

## ngcloud deck-api

- **Base URL:** `https://deck-api.ngcloud.ru/api/v1/index.cfm`
- **Auth:** `Authorization: Bearer <token>` on every request
- **HTTP timeout:** 30 s
- **Service ID for DNS Records:** `111`
- **Operation IDs:** create=`45`, delete=`46`, modify=`90`
- **Default TTL:** 120 s

### Create record flow (6 steps)

1. `GET /instanceOperations/default/45?fields=operation,svcOperationId,cfsParams` — fetch CFS param label→ID map (done once at startup, cached)
2. `POST /instances` `{serviceId, displayName, descr}` — create instance
3. `GET /instances?serviceId=111&...` — find instanceUID by displayName
4. `POST /instanceOperations` `{svcOperationId, instanceUid}` — create operation; operationUID extracted from `Location` response header via UUID regex
5. `POST /instanceOperationCfsParams` × 5 — push each CFS param separately
6. `POST /instanceOperations/{uid}/run` — start; then poll `GET /instanceOperations/{uid}` until `dtFinish != ""`

### Delete record flow

1. Find instanceUID by displayName
2. `POST /instanceOperations` `{svcOperationId: 46, instanceUid}`
3. `POST /instanceOperations/{uid}/run`
4. Poll same as above. No CFS params needed.

### CFS parameter labels (Russian, immutable — defined by the API)

| Label | Meaning |
|-------|---------|
| `"UUID Зоны"` | Zone UUID |
| `"Тип DNS-записи"` | Record type (A, CNAME, TXT, …) |
| `"Имя записи"` | Record name (FQDN) |
| `"Значение записи"` | Record value / IP |
| `"TTL записи (в секундах)"` | TTL in seconds |

### Poll completion logic

```
completed = dtFinish != "" && dtFinish != "null"
success   = isSuccessful == true
on failure: read errorLog field
```

### Listing records

Uses two endpoints that are assumed to exist (not confirmed by reference script — verify if issues arise):
- `GET /instanceOperations?instanceUid={uid}&page=1&pageSize=1` — get latest operation for an instance
- `GET /instanceOperationCfsParams?instanceOperationUid={uid}` — get CFS param values for that operation

## Instance naming

Each ngcloud instance represents one DNS record value (one target). DisplayName:
- First/only target: `dnsrecord-<dnsName>`
- Additional targets: `dnsrecord-<dnsName>-1`, `dnsrecord-<dnsName>-2`, …

`DeleteAllByName(name)` deletes all instances matching `dnsrecord-<name>` or `dnsrecord-<name>-*`.

## Zone mapping

ngcloud needs a zone UUID per record; external-dns works with zone names. Configured via env var `NGCLOUD_ZONE_MAP=example.com=<uuid>,foo.bar=<uuid>`. Resolved at record creation by longest-suffix match against `DNSName`.

## Webhook API contract

| Method | Path | Handler |
|--------|------|---------|
| GET | `/` | `Negotiate` — returns `DomainFilter` |
| GET | `/healthz` | `Healthz` — 200 OK |
| GET | `/records` | `GetRecords` — returns `[]*Endpoint` |
| POST | `/records` | `ApplyChanges` — apply `Changes` |
| POST | `/adjustendpoints` | `AdjustEndpoints` — passthrough |

Content-Type: `application/external.dns.webhook+json;version=1`

## Update strategy

Updates are implemented as **delete-all-old + create-all-new**. The modify operation (ID 90) is not used — its CFS structure is unconfirmed.

## Key env vars

| Var | Default | Notes |
|-----|---------|-------|
| `NGCLOUD_TOKEN` | — | Required |
| `NGCLOUD_ZONE_MAP` | — | Required, `zone=uuid` pairs |
| `NGCLOUD_BASE_URL` | `https://deck-api.ngcloud.ru/api/v1/index.cfm` | |
| `NGCLOUD_SERVICE_ID` | `111` | |
| `NGCLOUD_OP_CREATE` | `45` | |
| `NGCLOUD_OP_DELETE` | `46` | |
| `NGCLOUD_DEFAULT_TTL` | `120` | seconds |
| `DOMAIN_FILTER` | `""` | comma-separated |
| `SERVER_PORT` | `8888` | |
| `POLL_MAX_ATTEMPTS` | `60` | |
| `POLL_INTERVAL` | `5s` | |

## Reference

- `dns_record_create.sh` — shell script in repo root; used only as a working example of how the deck-api works. Not part of the application.
- [cert-manager-webhook-ngcloud](https://github.com/drengskapr/cert-manager-webhook-ngcloud) — sibling project by the same author; confirmed the `svcOperationId` field in `POST /instanceOperations`, the 30 s timeout, and the poll logic.
- Test DNS nameserver: `185.247.187.83:53` (ns3.ngcloud.ru)
