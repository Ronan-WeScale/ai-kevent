# kevent-ai

**kevent-ai** is an AI inference job gateway for Kubernetes/Knative. It exposes an HTTP API that accepts file uploads, enqueues them as Kafka jobs, and returns results asynchronously — or synchronously for low-latency use cases.

## Quick start

```bash
# Async job — fire and forget
curl -X POST https://your-gateway/jobs/audio \
  -F file=@audio.wav \
  -F model=whisper-large-v3

# Poll for result
curl https://your-gateway/jobs/audio/{job_id}

# Sync (OpenAI-compatible)
curl -X POST https://your-gateway/v1/audio/transcriptions \
  -F file=@audio.wav \
  -F model=whisper-large-v3
```

## Two operating modes

| Mode | Endpoint | When to use |
|---|---|---|
| **Async** | `POST /jobs/{service_type}` | Large files, batch workloads, fire-and-forget |
| **Sync** | `POST /v1/*` | Low-latency, OpenAI-compatible clients |

Sync mode has two sub-modes depending on config:

- **Direct proxy** — HTTP proxy to `inference_url` (JSON or multipart without `sync_topic`)
- **Sync-over-Kafka** — priority Kafka round-trip with keep-alive response (multipart + `sync_topic`)

## Components

Two independent Go binaries, two Docker images:

| Component | Image | Entry point |
|---|---|---|
| **Gateway** | `ghcr.io/ia-generative/kevent-ai/gateway` | `cmd/gateway/main.go` |
| **Relay** | `ghcr.io/ia-generative/kevent-ai/relay` | `relay/cmd/relay/main.go` |

The **relay** runs as a sidecar in each Knative InferenceService pod. It consumes Kafka jobs, calls the local model, and publishes results.

## Key features

- Config-driven service registry — add a new model with a YAML block, no code change
- Hot-reload via `POST /-/reload` — update config without pod restart
- Priority routing — dedicated Kafka topic for SA/priority consumers
- Consumer tracking — link jobs to API consumers via a configurable header
- Prometheus metrics — requests, latency, Kafka errors, Redis operations, jobs by consumer
- OpenAPI 3.0 spec generated at runtime from the live registry
- AES-256-GCM at-rest encryption for S3 objects

## Links

- [Architecture overview](architecture/overview.md)
- [Helm deployment](deployment/helm.md)
- [Configuration reference](deployment/configuration.md)
- [Runbooks](runbooks/KeventGatewayHighErrorRate.md)
- [Changelog](changelog.md)
