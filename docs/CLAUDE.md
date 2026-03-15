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
  client_test.go                # integration tests against live API
internal/webhook/
  types.go                      # Endpoint, Changes, DomainFilter (external-dns JSON schema)
  handlers.go                   # HTTP handler funcs
  handlers_test.go              # integration tests against live API via httptest
  server.go                     # ServeMux routing, contentType constant
```

## ngcloud deck-api

- **Base URL:** `https://deck-api.ngcloud.ru/api/v1/index.cfm`
- **Auth:** `Authorization: Bearer <token>` on every request
- **HTTP timeout:** 30 s
- **Service ID for DNS Records:** `111`
- **Operation IDs:** create=`45`, delete=`46`
- **Default TTL:** 120 s

### Create record flow

1. `GET /instanceOperations/default/45?fields=operation,svcOperationId,cfsParams` — fetch CFS param label→ID map (done once at startup, cached in `Client.cfsCreate`)
2. `POST /instances` `{serviceId, displayName, descr}` — create instance; if "not unique" (display name taken by a deleted instance), retry with a random suffix: `dnsrecord-<name>-<hex>`
3. `GET /instances?serviceId=111&...` — find instanceUID by displayName (first match)
4. `POST /instanceOperations` `{svcOperationId, instanceUid, operation: "create"}` — create operation; operationUID extracted from `Location` response header via UUID regex. The `operation` field is **required** — omitting it causes HTTP 500.
5. `POST /instanceOperationCfsParams` × 5 — push each CFS param separately
6. `POST /instanceOperations/{uid}/run` — start; **always returns HTTP 500** ("key [EXECUTABLE] doesn't exist") but the job still queues. Ignore this error and proceed to polling.
7. Poll `GET /instanceOperations/{uid}` every 5 s until `dtFinish != "" && dtFinish != "null"`; check `isSuccessful`.

### Delete record flow

1. `ListRecords()` to find instances by `recordName` CFS param (NOT display name — see pitfalls)
2. `POST /instanceOperations` `{svcOperationId: 46, instanceUid, operation: "delete"}`
3. `POST /instanceOperations/{uid}/run`
4. Poll until `isSuccessful`. No CFS params needed.

### Listing records

`GET /instances?serviceId=111` (no `fields` filter) returns `lastOperation` and `lastOperationUid` per instance. Active records have `lastOperation == "create"`. For each active instance, fetch CFS params:

```
GET /instanceOperationCfsParams?instanceOperationUid={uid}
```

This returns params keyed by `svcOperationCfsParam` (internal name, **not** the Russian label). Internal names used for reading:

| Internal name | Meaning |
|---------------|---------|
| `zoneUid` | Zone UUID |
| `recordType` | Record type (A, CNAME, TXT, …) |
| `recordName` | Relative record name (zone suffix NOT included) |
| `recordInput` | Record value / IP / CNAME target |
| `recordTTL` | TTL in seconds |

### CFS parameter labels (Russian) — used only for CREATE

These labels are used to look up `svcOperationCfsParamId` from `fetchCFSParamDefs` and to push params during creation:

| Label | Meaning |
|-------|---------|
| `"UUID Зоны"` | Zone UUID |
| `"Тип DNS-записи"` | Record type |
| `"Имя записи"` | Relative record name |
| `"Значение записи"` | Record value |
| `"TTL записи (в секундах)"` | TTL in seconds |

### Confirmed broken endpoints

- **`GET /instanceOperations?instanceUid=...`** — returns SQL error "operator does not exist: uuid = character varying". Never use this.

### Known API quirks

- **`POST /run` always returns HTTP 500** — ignore; job still queues. Poll for actual outcome.
- **`recordName` must be relative** — e.g. `webhook-test`, not `webhook-test.aillm.ru`. The API appends the zone suffix automatically.
- **`recordName` has `uniqueScope: "parent"`** — only one instance per record name per zone. Multi-target records (two IPs for same hostname) are not supported.
- **Deleted instances permanently hold their display names** — the uniqueness index includes deleted instances. Display names can never be reused. Use randomised fallback names and find instances to delete by `recordName` CFS param, not display name.
- **`GET /instances` list pagination** — response field is `"total"` (not `"totalCount"`).
- **Deleted instances remain in the list** — filter by `isDeleted: false`.

### CNAME record values

CNAME targets must end with a trailing dot when stored (e.g. `target.example.com.`). The webhook appends `.` on write and strips it on read so external-dns always sees the dot-free form.

## Instance naming

Each ngcloud instance represents one DNS record. `displayName` is `dnsrecord-<relativeName>`. If that name is taken (deleted instance holding the name), a random hex suffix is appended: `dnsrecord-<relativeName>-<hex>`.

`DeleteAllByName(name)` finds instances via `ListRecords()` matching `rec.Name == name`, then deletes each by UID. It does **not** rely on display name prefix.

## Zone mapping

ngcloud needs a zone UUID per record; external-dns works with zone names. Configured via env var `NGCLOUD_ZONE_MAP=example.com=<uuid>,foo.bar=<uuid>`. Resolved at record creation by longest-suffix match against `DNSName`. Zone suffix is stripped from `DNSName` to get the relative record name.

## Webhook API contract

| Method | Path | Handler |
|--------|------|---------|
| GET | `/` | `Negotiate` — returns `DomainFilter` |
| GET | `/healthz` | `Healthz` — 200 OK |
| GET | `/records` | `GetRecords` — returns `[]*Endpoint` |
| POST | `/records` | `ApplyChanges` — apply `Changes` |
| POST | `/adjustendpoints` | `AdjustEndpoints` — caps to single target |

Content-Type: `application/external.dns.webhook+json;version=1`

`AdjustEndpoints` truncates each endpoint to a single target (platform limitation: `recordName` uniqueness constraint).

## Update strategy

Updates are **delete-old + create-new**. `DeleteAllByName` finds and deletes the old instance. `CreateRecord` then creates the new one, using a randomised display name if the original is taken.

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

## Testing

Integration tests require live API credentials. All tests skip if env vars are absent.

**Required env vars:**

| Var | Example |
|-----|---------|
| `NGCLOUD_TOKEN` | Bearer JWT |
| `TEST_ZONE_UID` | UUID of the test zone |
| `TEST_ZONE_NAME` | e.g. `aillm.ru` |

**Run:**
```
NGCLOUD_TOKEN=... TEST_ZONE_UID=... TEST_ZONE_NAME=... \
  go test -v -timeout 20m ./internal/ngcloud/ ./internal/webhook/
```

Each create or delete operation takes ~60 s to poll to completion. The full suite takes ~8 minutes.

**After a failed test run**, zombie instances (non-deleted, no state) may block subsequent runs. Remove them manually via the ngcloud UI before re-running.

## Reference

- `dns_record_create.sh` — shell script in repo root; working example of the deck-api flow. Not part of the application.
- [cert-manager-webhook-ngcloud](https://github.com/drengskapr/cert-manager-webhook-ngcloud) — sibling project; confirmed `svcOperationId` field, 30 s timeout, poll logic.
- Test DNS nameserver: `185.247.187.83:53` (ns3.ngcloud.ru)
