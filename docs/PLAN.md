# Implementation Plan: external-dns webhook for ngcloud.ru

## Status: implemented and integration-tested

All core functionality is complete and passing against the live ngcloud deck-api.

---

## Project structure

```
.
├── main.go
├── go.mod / go.sum
├── Dockerfile
├── Makefile
├── internal/
│   ├── ngcloud/
│   │   ├── client.go        # HTTP client, auth, all deck-api calls
│   │   ├── client_test.go   # live integration tests
│   │   └── types.go         # request/response structs for deck-api
│   └── webhook/
│       ├── server.go        # HTTP server, routes, middleware
│       ├── handlers.go      # GET /, GET /healthz, GET /records, POST /records, POST /adjustendpoints
│       ├── handlers_test.go # live integration tests via httptest
│       └── types.go         # Endpoint, Changes, DomainFilter (matching external-dns JSON schema)
```

---

## Webhook API contract

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Returns `DomainFilter` JSON; used for negotiation |
| `GET` | `/healthz` | Health check → 200 OK |
| `GET` | `/records` | Returns `[]*Endpoint` of all current DNS records |
| `POST` | `/records` | Receives `Changes{Create, UpdateOld, UpdateNew, Delete}`, applies them |
| `POST` | `/adjustendpoints` | Truncates each endpoint to a single target (platform limitation) |

Content-Type: `application/external.dns.webhook+json;version=1`

---

## ngcloud deck-api — confirmed behavior

**Base URL:** `https://deck-api.ngcloud.ru/api/v1/index.cfm`
**Auth:** `Authorization: Bearer <token>` on every request
**HTTP client timeout:** 30 seconds
**Service ID (DNS Records):** `111`
**Operation IDs:** create=`45`, delete=`46`
**Default TTL:** 120 seconds

### CFS parameter fetch (startup)

```
GET /instanceOperations/default/{svcOperationId}?fields=operation,svcOperationId,cfsParams
```

Returns CFS param definitions with Russian `label` → `svcOperationCfsParamId`. Labels used for CREATE:
- `"UUID Зоны"` — zone UUID
- `"Тип DNS-записи"` — record type (A, CNAME, TXT, …)
- `"Имя записи"` — relative record name (no zone suffix)
- `"Значение записи"` — record value / IP / CNAME target
- `"TTL записи (в секундах)"` — TTL

### Create record flow (confirmed)

1. `GET /instanceOperations/default/45?fields=...` → fetch CFS param label→ID map (cache at startup)
2. `POST /instances` `{serviceId: 111, displayName: "dnsrecord-<name>", descr: ""}` → create instance
   - If "not unique" error: retry with randomised display name `dnsrecord-<name>-<hex>` (deleted instances permanently hold their display names)
3. `GET /instances?serviceId=111&page=1&pageSize=100` → find instanceUID by displayName
4. `POST /instanceOperations` `{svcOperationId: 45, instanceUid: "<uid>", operation: "create"}` → create operation; extract operationUID from `Location` response header via UUID regex
   - **`operation` field is required** — omitting it causes HTTP 500
5. `POST /instanceOperationCfsParams` × 5 — one request per CFS param
6. `POST /instanceOperations/{operationUID}/run` → **always returns HTTP 500** ("key [EXECUTABLE] doesn't exist") but the job still queues — ignore this error
7. Poll `GET /instanceOperations/{operationUID}` every 5 s (max 60 attempts) until `dtFinish != "" && dtFinish != "null"`; check `isSuccessful`

### Delete record flow (confirmed)

1. Call `ListRecords()` to find the instance UID by `recordName` CFS param (display name must NOT be used — deleted instances retain their names permanently)
2. `POST /instanceOperations` `{svcOperationId: 46, instanceUid: "<uid>", operation: "delete"}`
3. `POST /instanceOperations/{operationUID}/run`
4. Poll until completion (same as create). No CFS params needed.

### List records flow (confirmed)

1. `GET /instances?serviceId=111&pageSize=100` (no `fields` filter) → returns all instances including `lastOperation`, `lastOperationUid`, `isDeleted`; pagination via `page` param; total count in `"total"` field (not `"totalCount"`)
2. Filter: `!isDeleted && lastOperation == "create"`
3. For each active instance: `GET /instanceOperationCfsParams?instanceOperationUid={uid}` → returns params with `svcOperationCfsParam` (internal name) and `paramValue`
4. Build `Record` from internal names: `zoneUid`, `recordType`, `recordName`, `recordInput`, `recordTTL`

### Poll completion logic

```
completed = dtFinish != "" && dtFinish != "null"
success   = isSuccessful == true
```

### Confirmed broken endpoints

- **`GET /instanceOperations?instanceUid=...`** — SQL error "operator does not exist: uuid = character varying". Never use.

### Instance lifecycle — platform limitation

`DELETE /instances/{uid}` returns HTTP 422 "Instance deletion disabled when having state" for any instance that has had an operation run on it. Instances cannot be removed via the API once they have state.

After a DNS delete operation completes, the instance remains in the ngcloud UI with `lastOperation: "delete"`. The DNS record is gone from the nameserver; only the platform object persists. These instances are invisible to `ListRecords` and do not affect DNS or future runs. Manual removal via the ngcloud UI is the only option.

---

## Key design decisions and platform constraints

### Single target per record name (platform limitation)

The `recordName` CFS param has `uniqueScope: "parent"` — the API enforces uniqueness of record name per zone. Only one ngcloud instance can exist per DNS record name. Multi-target records (two A records for the same hostname) are **not supported**.

`AdjustEndpoints` truncates each endpoint to a single target so external-dns never sends multi-target records.

### Display name reuse is impossible

Deleted instances permanently hold their display names in the uniqueness index. `CreateRecord` falls back to a randomised display name (`dnsrecord-<name>-<hex>`) on "not unique" errors. `DeleteAllByName` never relies on display name prefix — it matches by `recordName` CFS param via `ListRecords()`.

### Relative record names

`recordName` must be the name relative to the zone (e.g. `webhook-test`, not `webhook-test.aillm.ru`). The API appends the zone suffix automatically.

### CNAME trailing dot

The ngcloud DNS backend requires CNAME targets to end with a trailing dot (e.g. `target.example.com.`). The webhook appends `.` when writing and strips it when reading.

### Update strategy

Delete-old + create-new. No atomic modify operation. `DeleteAllByName` waits for the delete to complete (polling), then `CreateRecord` creates the new instance.

### Synchronous polling

`POST /records` blocks until all ngcloud operations complete. external-dns expects synchronous completion.

### Logging

`k8s.io/klog/v2` with ISO8601 timestamps.

---

## Zone mapping

ngcloud requires a zone UUID per record; external-dns works with zone names. Configured via `NGCLOUD_ZONE_MAP=example.com=<uuid>`. Zone suffix is stripped from `DNSName` by longest-suffix match to get the relative record name.

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
| `NGCLOUD_DEFAULT_TTL` | `120` | Default TTL in seconds |
| `DOMAIN_FILTER` | `""` | Comma-separated domain filter for external-dns |
| `SERVER_PORT` | `8888` | Webhook HTTP port |
| `POLL_MAX_ATTEMPTS` | `60` | Operation polling max attempts |
| `POLL_INTERVAL` | `5s` | Sleep between poll attempts |

---

## Testing

Integration tests in `internal/ngcloud/client_test.go` and `internal/webhook/handlers_test.go` run against the live deck-api. All tests skip if credentials are absent.

**Required env vars:**

| Var | Description |
|-----|-------------|
| `NGCLOUD_TOKEN` | Bearer JWT |
| `TEST_ZONE_UID` | UUID of the test DNS zone |
| `TEST_ZONE_NAME` | Name of the test DNS zone (e.g. `aillm.ru`) |

**Run all tests:**
```
NGCLOUD_TOKEN=... TEST_ZONE_UID=... TEST_ZONE_NAME=... \
  go test -v -timeout 20m ./internal/ngcloud/ ./internal/webhook/
```

Each create or delete operation polls for ~60 s. Full suite takes ~8 minutes.

### Test coverage (all passing)

**`internal/ngcloud/`**
- `TestCreateDeleteRecord` — A record create + delete
- `TestCreateDeleteTXTRecord` — TXT record create + delete
- `TestCreateDeleteCNAMERecord` — CNAME record create + delete (trailing dot applied automatically)
- `TestListRecords` — list all active records via CFS params
- `TestCreateListDelete` — create, verify in list, delete

**`internal/webhook/`**
- `TestNegotiate` — GET / returns DomainFilter
- `TestHealthz` — GET /healthz returns 200
- `TestAdjustEndpoints` — POST /adjustendpoints passthrough
- `TestGetRecords` — GET /records against live API
- `TestApplyChangesCreateDelete` — POST /records create + verify + delete
- `TestApplyChangesUpdate` — POST /records update (delete-old + create-new)
- `TestApplyChangesCNAME` — CNAME create/verify (no trailing dot in request)/delete

### Known test hygiene issue

If a test run fails mid-way, zombie instances (non-deleted, no state, `lastOperation=null` or failed delete) may remain. They cannot be deleted via the API (no state to operate on) and block new creates with the same display name. Remove them manually via the ngcloud UI before re-running.
