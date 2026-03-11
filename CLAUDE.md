# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository overview

**kevent** is an AI inference job gateway for Kubernetes/Knative. It exposes an HTTP API that accepts file uploads, enqueues them as Kafka jobs, and returns results asynchronously. A sidecar dispatcher runs inside each Knative InferenceService pod, consuming jobs from Kafka, calling the local inference model, and publishing results back.

Two independent Go modules (separate `go.mod`, separate Docker images):
- **Gateway** — root module `kevent/gateway`, entry point `cmd/gateway/main.go`
- **Dispatcher** — module `kevent/dispatcher` in `./dispatcher/`, entry point `dispatcher/cmd/dispatcher/main.go`

## Build commands

```bash
# Gateway
go build ./cmd/gateway          # from repo root
go vet ./...
go test ./...

# Dispatcher
cd dispatcher
go build ./cmd/dispatcher
go vet ./...
go test ./...
```

### Docker images

Images must be rebuilt and pushed after any code change, then tags updated in `values.yaml` and `k8s/inference-transcription.yaml`:

```bash
# Gateway  →  tcheksa62/kevent:<tag>
docker build -t tcheksa62/kevent:vX.Y.Z .
docker push  tcheksa62/kevent:vX.Y.Z

# Dispatcher  →  tcheksa62/side-event:<tag>
docker build -t tcheksa62/side-event:vX.Y.Z ./dispatcher
docker push  tcheksa62/side-event:vX.Y.Z
```

Current tags: gateway `v0.2.4`, dispatcher `v0.2.4`.

After a push, update:
1. `values.yaml` → `image.tag`
2. `k8s/inference-transcription.yaml` → `spec.containers[name=dispatcher].image`
3. Commit to git

## Architecture

### Request flow

```
Client
  │  POST /jobs (multipart)
  ▼
Gateway (:8080)
  ├── Upload file → Scaleway S3 (bucket: test-kevent-jobs)
  ├── Save job record → Redis (TTL 72h)
  └── Publish InputEvent → Kafka topic jobs.<type>.input
                                    │
                              KafkaSource (Knative Eventing)
                                    │  CloudEvent HTTP POST
                                    ▼
                         Dispatcher sidecar (:8080, inside InferenceService pod)
                              ├── Download file from S3
                              ├── POST to local inference model (127.0.0.1:9000)
                              ├── Upload result.json → S3
                              └── Publish ResultEvent → Kafka topic jobs.<type>.results
                                                                │
                                                         Gateway ConsumerManager
                                                              └── Update Redis → trigger webhook
```

### Sync (OpenAI-compatible) mode

Gateway also proxies `POST /v1/*` directly to the InferenceService cluster URL (no Kafka), routing by the `model` field in the request body. The dispatcher sidecar forwards `/v1/*` straight to the local inference model. Both are configured via `services[].openai_path`, `services[].model`, `services[].inference_url` in `config.yaml`.

### Config loading

Both binaries use `config.Load(path)` which reads a YAML file and expands `${VAR}` / `${VAR:-default}` with `os.Expand` before unmarshalling. The config path defaults to `config.yaml` in the working directory, overridden by env var `CONFIG_PATH`.

**Adding a new service type** requires only a new entry in `config.yaml` (and `values.yaml` for Helm). No Go code change is needed — the service registry (`internal/service/registry.go`) is entirely config-driven.

### Kafka authentication (SASL/TLS)

`github.com/segmentio/kafka-go` v0.4.47 is used for both reading and writing.

- Readers use `kafkago.Dialer` (built by `internal/kafka/auth.go:buildDialer`)
- Writers use `kafkago.Transport` (built by `internal/kafka/auth.go:buildTransport`)

**Critical**: `buildTransport` returns `(*kafkago.Transport, error)` where transport can be `nil` when SASL/TLS is not configured. Never assign the result directly to `w.Transport` (a `RoundTripper` interface) — a typed nil pointer produces a non-nil interface value and panics. Use:
```go
if p.transport != nil {
    w.Transport = p.transport
}
```

Broker: `default-kafka-bootstrap.infra-kafka.svc.cluster.local:9093` (SASL_SSL, SCRAM-SHA-512).

### Strimzi KafkaUsers (`k8s/kafka-users.yaml`)

- `kevent-gateway` (namespace `infra-kafka`) — Write/Read on `jobs.*` topics, Read on `kevent-gateway*` groups
- `kevent-dispatcher` (namespace `infra-kafka`) — Read on `jobs.*` topics, **Read + Describe + Delete** on `inference-*` groups (Delete is required by the Knative controller for ConsumerGroup finalization), Write on `jobs.*` topics

### Secret hygiene

Strimzi-generated secrets (e.g. `kevent-dispatcher-kafka`) must not have trailing newlines in values. The Knative KafkaSource controller does exact string comparison on `sasl-type` — a `\n` suffix causes `[protocol SASL_SSL] unsupported SASL mechanism`. Verify with:
```bash
kubectl get secret kevent-dispatcher-kafka -n default \
  -o jsonpath='{.data.sasl-type}' | base64 -d | xxd
```

## Key files

| File | Purpose |
|---|---|
| `config.yaml` | Gateway config template (env-expanded at startup) |
| `dispatcher/config.yaml` | Dispatcher config template |
| `values.yaml` | Helm values for production deployment |
| `helm/gateway/` | Helm chart — generates ConfigMap, Secret, Deployment, Ingress |
| `k8s/kafka-users.yaml` | Strimzi KafkaUser ACLs (apply in `infra-kafka` namespace) |
| `k8s/inference-transcription.yaml` | KServe InferenceService + ServingRuntime for Whisper |
| `internal/service/registry.go` | Config-driven service registry (no code change needed to add services) |
| `internal/kafka/auth.go` | SASL/TLS dialer+transport construction for gateway |
| `dispatcher/internal/kafka/auth.go` | SASL/TLS transport construction for dispatcher |

## Deployment

Helm chart deploys the gateway with Redis-HA (HAProxy front-end). The dispatcher runs as a sidecar in the `ServingRuntime` (KServe), not managed by Helm.

```bash
# Deploy / upgrade gateway
helm upgrade --install kevent-gateway ./helm/gateway -f values.yaml

# Apply Strimzi users (namespace infra-kafka)
kubectl apply -f k8s/kafka-users.yaml -n infra-kafka

# Apply InferenceService + ServingRuntime
kubectl apply -f k8s/inference-transcription.yaml
```
