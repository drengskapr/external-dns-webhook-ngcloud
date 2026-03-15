External DNS webhook Ngcloud
===

An [external-dns webhook provider](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/) for [ngcloud.ru](https://ngcloud.ru) (Nubes cloud). Runs as a sidecar to external-dns and translates `Endpoint` objects into ngcloud deck-api calls.

## How it works

The webhook implements the external-dns webhook provider HTTP contract. External-dns calls it to negotiate domain filters, list current DNS records, and apply changes (create/update/delete). The webhook translates these into ngcloud deck-api operations, polling each one to completion before responding.

**Platform constraints:**

* One ngcloud instance per DNS record name per zone (no multi-target A/AAAA records). `AdjustEndpoints` caps each endpoint to a single target.

* Updates are delete-old + create-new (no atomic modify).

* After a DNS delete operation completes, the ngcloud instance object persists in the platform UI indefinitely — the API forbids deleting instances that have state. The DNS record is fully removed; only the platform object lingers.

## Configuration

All configuration is via environment variables:

| Variable              | Default | Description |
|-----------------------|---------|-------------|
| `NGCLOUD_TOKEN`       | —       | Bearer token (**required**) |
| `NGCLOUD_ZONE_MAP`    | —       | Comma-separated `zone=uuid` pairs (**required**), e.g. `example.com=<uuid>,foo.bar=<uuid>` |
| `NGCLOUD_BASE_URL`    | `https://deck-api.ngcloud.ru/api/v1/index.cfm` | deck-api base URL |
| `NGCLOUD_SERVICE_ID`  | `111`   | DNS Records service ID |
| `NGCLOUD_OP_CREATE`   | `45`    | Create operation ID |
| `NGCLOUD_OP_DELETE`   | `46`    | Delete operation ID |
| `NGCLOUD_DEFAULT_TTL` | `120`   | Default TTL in seconds |
| `DOMAIN_FILTER`       | `""`    | Comma-separated domain filter passed to external-dns negotiation |
| `SERVER_PORT`         | `8888`  | Webhook HTTP port |
| `POLL_MAX_ATTEMPTS`   | `60`    | Max polling attempts per operation |
| `POLL_INTERVAL`       | `5s`    | Interval between poll attempts |

## Building

**Binary:**
```bash
go build -o external-dns-webhook-ngcloud .
```

**Docker image:**
```bash
docker build -t drengskapr/external-dns-webhook-ngcloud:latest .
# or with overrides:
IMAGE=registry.example.com/external-dns-webhook-ngcloud TAG=v1.0.0 make docker-build
```

## Testing

Integration tests run against the live ngcloud deck-api. All tests skip automatically if credentials are absent.

**Required environment variables:**

| Variable         | Description |
|------------------|-------------|
| `NGCLOUD_TOKEN`  | Bearer JWT |
| `TEST_ZONE_UID`  | UUID of the test DNS zone |
| `TEST_ZONE_NAME` | Name of the test DNS zone |

**Run:**
```bash
NGCLOUD_TOKEN=... TEST_ZONE_UID=... TEST_ZONE_NAME=... \
  go test -v -count=1 -timeout 20m ./internal/ngcloud/ ./internal/webhook/
```

Each create or delete operation polls for up to 5 minutes. The full suite takes roughly 5–8 minutes.

After the test suite finishes, spent ngcloud instances (with `lastOperation: delete`) will remain visible in the ngcloud UI &mdash; this is a platform limitation (instances with state cannot be deleted via the API). They do not affect DNS or future test runs and can be cleaned up manually via the UI if desired.

## Deploying with external-dns

The webhook must run as a sidecar in the same pod as external-dns so they can communicate over localhost.

### 1. Create a Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: external-dns-ngcloud
  namespace: external-dns
stringData:
  NGCLOUD_TOKEN: "<bearer-token>"
  NGCLOUD_ZONE_MAP: "example.com=<zone-uuid>"
```

### 2. Deploy external-dns with the webhook sidecar

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-dns
  namespace: external-dns
spec:
  selector:
    matchLabels:
      app: external-dns
  template:
    metadata:
      labels:
        app: external-dns
    spec:
      containers:
        - name: external-dns
          image: registry.k8s.io/external-dns/external-dns:v0.20.0
          args:
            - --provider=webhook
            - --webhook-provider-url=http://localhost:8888
            - --source=ingress
            - --domain-filter=example.com
            - --log-level=info
        - name: webhook
          image: drengskapr/external-dns-webhook-ngcloud:0.1.0
          envFrom:
            - secretRef:
                name: external-dns-ngcloud
          ports:
            - containerPort: 8888
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8888
```

Adjust `--source` (e.g. `service`, `ingress`, `crd`) and `--domain-filter` to match your setup. external-dns RBAC (ClusterRole/ClusterRoleBinding/ServiceAccount) follows the [standard external-dns setup](https://github.com/kubernetes-sigs/external-dns) and is independent of this webhook.
