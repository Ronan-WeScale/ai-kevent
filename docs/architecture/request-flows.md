# Request flows

## Async mode

`POST /jobs/{service_type}` — fire and forget, poll for result.

```
Client
  │  POST /jobs/{service_type}
  │  (multipart: file, model?, operation?, callback_url?)
  ▼
Gateway (:8080)
  ├── Upload file ──────────────────────────────► S3
  ├── Save job record ──────────────────────────► Redis (TTL 72h)
  └── Publish InputEvent ───────────────────────► Kafka jobs.<model>.input
                                                          │
                                                  KafkaSource (async)
                                                  CloudEvent → POST /
                                                          │
                                                          ▼
                                               Relay sidecar (:8080)
                                                ├── syncPriority > 0?
                                                │   └── yes → 503 (KafkaSource retries)
                                                ├── Download file ◄──── S3
                                                ├── POST to model (127.0.0.1:9000)
                                                ├── Upload result ─────► S3
                                                └── Publish ResultEvent ► Kafka jobs.<model>.results
                                                                                  │
                                                                         Gateway ConsumerManager
                                                                           ├── Update Redis job
                                                                           ├── Notify pub/sub
                                                                           └── Webhook (callback_url)
```

**Client polling:**

```
GET /jobs/{service_type}/{job_id}
→ { status: "pending" | "completed" | "failed", result: {...} }
```

---

## Sync-over-Kafka mode

`POST /v1/*` multipart with `sync_topic` configured. The connection stays open; result is returned inline.

```
Client
  │  POST /v1/audio/transcriptions (multipart, keep-alive)
  ▼
Gateway
  ├── Upload file ──────────────────────────────► S3
  ├── Save job ────────────────────────────────► Redis
  ├── Subscribe pub/sub job:<id>:done           ← before publishing
  ├── Publish InputEvent ───────────────────────► Kafka jobs.<model>.sync
  └── Wait on pub/sub ─────────────────────────────────────────────────────┐
                                                          │                 │
                                                  KafkaSource (sync)        │
                                                  CloudEvent → POST /sync   │
                                                          │                 │
                                                          ▼                 │
                                               Relay sidecar                │
                                                ├── Set syncPriority++      │
                                                ├── Process job             │
                                                ├── Publish ResultEvent ────►│
                                                └── Unset syncPriority--    │
                                                                            │
                                                                 Gateway receives pub/sub
                                                                   ├── Fetch result from S3
                                                                   ├── Return 200 with result inline
                                                                   └── Cleanup (S3 + Redis)
```

---

## Sync direct proxy mode

`POST /v1/*` JSON body, or multipart without `sync_topic`.

```
Client
  │  POST /v1/audio/transcriptions
  │  { "model": "whisper-large-v3", ... }
  ▼
Gateway
  └── HTTP proxy ──────────────────────────────► InferenceService
                                                 (inference_url + original path)
                                                          │
                                                          ▼
                                                 Response proxied back
```

---

## Routing decision

```
POST /v1/* received
       │
       ├── Content-Type: multipart/form-data?
       │       ├── sync_topic configured for model?
       │       │       └── YES → sync-over-Kafka
       │       └── NO → direct proxy
       └── Content-Type: application/json
               └── direct proxy
```
