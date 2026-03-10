# KEVENT — Architecture

## Vue d'ensemble

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│                              KEVENT — Vue d'ensemble                              │
└──────────────────────────────────────────────────────────────────────────────────┘

  CLIENT
    │
    │  ① Mode ASYNC                    ② Mode SYNC (OpenAI-compatible)
    │  POST /jobs                       POST /v1/audio/transcriptions
    │  multipart: type + file           POST /v1/chat/completions
    │  GET  /jobs/{id}                  (payload OpenAI standard, field "model")
    │  Webhook (callback_url)
    ▼
┌────────────────────────────────────────────────────────────────────────────────┐
│  GATEWAY  (Deployment Kubernetes, chi router)                                  │
│                                                                                │
│   ┌─────────────────────────────────────────────────────────────────────────┐ │
│   │  JobHandler  (mode async)                  POST /jobs • GET /jobs/{id}  │ │
│   │    Submit :  S3.Upload → Redis.SaveJob → Kafka.Publish                  │ │
│   │    GetStatus: Redis.GetJob → S3.PresignedGetURL (si completed)          │ │
│   └─────────────────────────────────────────────────────────────────────────┘ │
│   ┌─────────────────────────────────────────────────────────────────────────┐ │
│   │  SyncHandler  (mode sync)                          POST /v1/*           │ │
│   │    ① Extrait "model" du payload (multipart ou JSON)                     │ │
│   │    ② registry.RouteSync(path, model) → InferenceURL                    │ │
│   │    ③ Proxy HTTP → Dispatcher Knative /v1/*                              │ │
│   │    ④ Stream réponse directement au client                               │ │
│   └─────────────────────────────────────────────────────────────────────────┘ │
│   ┌─────────────────────────────────────────────────────────────────────────┐ │
│   │  ConsumerManager  (1 goroutine / result topic)    mode async seulement  │ │
│   │    Kafka.FetchMessage ← jobs.{type}.results  (ResultEvent)              │ │
│   │    ① Redis.UpdateJobResult → status: completed|failed                   │ │
│   │    ② sendWebhook(callback_url)  ×3 avec backoff exponentiel            │ │
│   └─────────────────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────────────────┘
    │  S3 / Redis / Kafka                    │  HTTP proxy
    ▼  (mode async)                          ▼  (mode sync)
┌──────────────────┐                ┌────────────────────────────────────────────┐
│  Scaleway S3     │                │  Knative Services (1 par type de service)  │
│                  │                │                                            │
│  {job_id}/       │                │  kevent-dispatcher-transcription           │
│  input.ext  ◀────┼────────────────│  ┌──────────────────────┐ ┌────────────┐  │
│  result.json────▶│                │  │  dispatcher  :8080   │ │ whisper    │  │
└──────────────────┘                │  │  ─────────────────── │ │ :9000      │  │
                                    │  │  POST /   async       │ │            │  │
┌──────────────────┐                │  │   KafkaSource↗  ─────┼▶│  GPU       │  │
│  Kafka           │                │  │  POST /v1/* sync      │ │  inférence │  │
│                  │                │  │   Gateway↗      ─────┼▶│            │  │
│  jobs.*.input ───┼── KafkaSource─▶│  └──────────────────────┘ └────────────┘  │
│  jobs.*.results◀─┼────────────────│                                            │
└──────────────────┘                │  kevent-dispatcher-ocr  (même structure)  │
                                    └────────────────────────────────────────────┘
```

---

## Flux 1 — Soumission d'un job (mode async)

```
Client          Gateway          Redis         S3 (Scaleway)     Kafka
  │                │               │                │               │
  │─POST /jobs────▶│               │                │               │
  │  type=transcr  │               │                │               │
  │  file=audio.wav│               │                │               │
  │                │─ValidateFile  │                │               │
  │                │  (ext, size)  │                │               │
  │                │──PutObject────────────────────▶│               │
  │                │  abc123/input.wav              │               │
  │                │◀──────────────────────────────│               │
  │                │──SaveJob─────▶│               │               │
  │                │  {id, status: │               │               │
  │                │   pending}    │               │               │
  │                │◀──────────────│               │               │
  │                │──PublishInputEvent────────────────────────────▶│
  │                │  { job_id, input_ref }         │  jobs.transcr │
  │◀─202───────────│               │                │  .input       │
  │  { job_id,     │               │                │               │
  │    status:     │               │                │               │
  │    "pending" } │               │                │               │
```

---

## Flux 2 — Traitement asynchrone (dispatcher sidecar + GPU)

```
Kafka         KafkaSource       Dispatcher :8080   Whisper :9000    S3          Kafka
  │               │                  │                   │            │            │
  │  jobs.transcr │                  │                   │            │            │
  │  .input       │                  │                   │            │            │
  │──FetchMessage▶│                  │                   │            │            │
  │               │                  │                   │            │            │
  │               │  [cold start : readinessProbe attend que whisper soit ready]  │
  │               │                  │                   │            │            │
  │               │─POST /───────────▶                  │            │            │
  │               │  CloudEvent       │                   │            │            │
  │               │  { job_id,        │                   │            │            │
  │               │    input_ref }    │                   │            │            │
  │               │                  │                   │            │            │
  │               │              [handler bloque — Knative KPA voit 1 req en vol] │
  │               │                  │                   │            │            │
  │               │                  │──GetObject────────────────────▶│            │
  │               │                  │  abc123/input.wav │            │            │
  │               │                  │◀──────────────────────────────│            │
  │               │                  │  io.ReadCloser (stream)        │            │
  │               │                  │──POST /v1/audio/─▶│            │            │
  │               │                  │  transcriptions   │            │            │
  │               │                  │  (multipart pipe) │─GPU────────│            │
  │               │                  │                   │  inférence │            │
  │               │                  │◀──────────────────│  ~2-5 min  │            │
  │               │                  │  { text: "..." }  │            │            │
  │               │                  │──PutObject────────────────────▶│            │
  │               │                  │  abc123/result.json            │            │
  │               │                  │──PublishResultEvent────────────────────────▶│
  │               │                  │  { completed, result_ref }     │  jobs.    │
  │               │◀─200─────────────│                   │            │  .results │
  │◀─CommitOffset─│                  │                   │            │            │
```

---

## Flux 3 — Retour du résultat (mode async)

```
Kafka          Gateway                Redis           Client
  │         ConsumerManager             │               │
  │               │                     │               │
  │  jobs.transcr │                     │               │
  │  .results     │                     │               │
  │──FetchMessage▶│                     │               │
  │               │──UpdateJobResult────▶               │
  │               │  { status: completed,│               │
  │               │    result_ref }      │               │
  │◀─CommitOffset─│◀────────────────────│               │
  │               │──sendWebhook (goroutine)────────────▶│
  │               │  POST callback_url  │  retry ×3    │
  │               │  { job_id, status,  │  backoff ×2  │
  │               │    result_ref }     │               │
  │               │                     │               │
  │               │                     │               │◀─GET /jobs/abc123
  │               │◀────────────────────│──GetJob───────│
  │               │  PresignedGetURL    │               │
  │               │  (S3, TTL 60 min)   │               │
  │               │─────────────────────────────────────▶│
  │               │  { status: "completed",              │
  │               │    result_url: "https://s3.fr-par... │
  │               │    ?X-Amz-Expires=3600" }            │
```

---

## Flux 4 — Requête synchrone (proxy OpenAI-compatible)

```
Client (SDK OpenAI)     Gateway SyncHandler     Dispatcher /v1/*     Whisper :9000
       │                        │                       │                    │
       │─POST /v1/audio/────────▶                      │                    │
       │  transcriptions        │                       │                    │
       │  model=whisper-large-v3│                       │                    │
       │  file=audio.wav        │                       │                    │
       │                        │                       │                    │
       │                        │  ParseMultipartForm   │                    │
       │                        │  r.FormValue("model") │                    │
       │                        │  RouteSync(path,model)│                    │
       │                        │  → InferenceURL       │                    │
       │                        │                       │                    │
       │                        │─POST /v1/audio/───────▶                   │
       │                        │  transcriptions        │                    │
       │                        │  (body reconstruit     │                    │
       │                        │   via io.Pipe)         │                    │
       │                        │                        │─POST /v1/audio/───▶
       │                        │                        │  transcriptions    │
       │                        │                        │  (transparent fwd) │
       │                        │                        │                    │─GPU─▶
       │                        │                        │                    │ inférence
       │                        │                        │◀───────────────────│
       │                        │◀───────────────────────│  { text: "..." }   │
       │◀────────────────────────│  stream réponse        │                    │
       │  { text: "..." }        │                        │                    │
```

> Aucun passage par S3, Redis ou Kafka : la réponse est streamée bout-en-bout.

---

## Scaling Knative — comportement GPU

```
                        jobs.transcription.input  (lag Kafka)

  msgs  5 ──│                    ████
  en    4 ──│                ████████
  queue 3 ──│            ████████████
        2 ──│        ████████████████
        1 ──│    ████████████████████
        0 ──│────────────────────────────────────────────────▶ temps
             silence   burst          traitement   silence

  pods  0 ──│    ┌───┐
  actifs 1 ──│    │   └───┐
        2 ──│    │       └───┐
        3 ──│    │           └───┐
        4 ──│    │               └───┐
        5 ──│    │                   └───────────────────────▶
             ▲   ▲                   ▲           ▲
          scale  cold start        traitement  scale
          to 0   (~30s model load)  en cours   to 0
                                               (scaleToZeroGracePeriod)

  GPU   $$  0   [$][$][$][$][$]    [$][$][$]  0
  coût  ────────────────────────────────────────────▶ temps

  containerConcurrency: 1  →  1 message en vol = 1 pod = 1 GPU
  maxScale: N              →  jamais plus de N pods/GPUs simultanés
```

> En mode sync, le KPA Knative mesure les requêtes HTTP en cours sur le dispatcher.
> Une requête de transcription = 1 pod GPU occupé pendant toute la durée de l'inférence.

---

## Structure des données

### Topics Kafka (mode async)

| Topic | Produit par | Consommé par | Clé message |
|---|---|---|---|
| `jobs.transcription.input` | Gateway | KafkaSource → dispatcher | `job_id` |
| `jobs.diarization.input` | Gateway | KafkaSource → dispatcher | `job_id` |
| `jobs.ocr.input` | Gateway | KafkaSource → dispatcher | `job_id` |
| `jobs.transcription.results` | Dispatcher | Gateway ConsumerManager | `job_id` |
| `jobs.diarization.results` | Dispatcher | Gateway ConsumerManager | `job_id` |
| `jobs.ocr.results` | Dispatcher | Gateway ConsumerManager | `job_id` |

### Objets S3 (mode async)

```
kevent-jobs/
└── {job_id}/
    ├── input.wav          ← uploadé par le gateway au POST /jobs
    └── result.json        ← uploadé par le dispatcher après inférence
```

### État Redis (TTL 72h, mode async uniquement)

```json
{
  "id":           "abc123",
  "service_type": "transcription",
  "status":       "pending | processing | completed | failed",
  "input_ref":    "abc123/input.wav",
  "result_ref":   "abc123/result.json",
  "callback_url": "https://client.example.com/webhook",
  "error":        "",
  "created_at":   "2026-03-05T10:00:00Z",
  "updated_at":   "2026-03-05T10:05:32Z"
}
```

### Routing sync (registre en mémoire)

```
openai_path                      model                    InferenceURL
/v1/audio/transcriptions   →   whisper-large-v3   →   http://kevent-dispatcher-transcription…
/v1/chat/completions        →   llava-v1.6-…       →   http://kevent-dispatcher-ocr…
```

Plusieurs services peuvent partager le même `openai_path` (ex: deux modèles sur `/v1/chat/completions`) — la sélection se fait uniquement par la valeur du champ `model`.

---

### Sémantique des codes HTTP du dispatcher (mode async)

| Code | Signification | Comportement KafkaSource |
|---|---|---|
| `200` | Job traité (succès ou échec métier publié via ResultEvent) | Offset commité, pas de retry |
| `400` | Message malformé (JSON invalide, job_id manquant) | Pas de retry |
| `500` | Erreur infrastructure transitoire (S3 indisponible, réseau) | Retry selon delivery config |

---

### Knative Service spec (exemple transcription)

```yaml
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: kevent-dispatcher-transcription
spec:
  template:
    metadata:
      annotations:
        autoscaling.knative.dev/max-scale: "5"
        autoscaling.knative.dev/scale-to-zero-pod-retention-period: "2m"
    spec:
      containerConcurrency: 1    # 1 requête par pod = 1 GPU par job
      timeoutSeconds: 600        # 10 min max — s'applique aux deux modes (async et sync)
      containers:

        - name: dispatcher
          image: kevent-dispatcher:latest
          ports:
            - name: http1          # http1 = HTTP/1.1 (pas h2c) pour queue-proxy
              containerPort: 8080
              protocol: TCP
          env:
            - name: SERVICE_TYPE
              value: transcription
            - name: INFERENCE_PORT
              value: "9000"
            - name: S3_ACCESS_KEY
              valueFrom: {secretKeyRef: {name: scaleway, key: access-key}}
            - name: S3_SECRET_KEY
              valueFrom: {secretKeyRef: {name: scaleway, key: secret-key}}
            - name: KAFKA_BROKERS
              value: "kafka:9092"
          readinessProbe:          # TCP dial sur 127.0.0.1:9000 — bloque jusqu'à ce
            httpGet:               # que whisper soit prêt (évite la race condition)
              path: /health
              port: 8080

        - name: whisper
          image: ghcr.io/your-org/whisper-server:latest
          ports:
            - containerPort: 9000
          resources:
            limits:
              nvidia.com/gpu: "1"
          readinessProbe:
            httpGet:
              path: /health
              port: 9000
            initialDelaySeconds: 30
```

### KafkaSource spec (exemple transcription)

```yaml
apiVersion: sources.knative.dev/v1
kind: KafkaSource
metadata:
  name: transcription-source
spec:
  bootstrapServers: ["kafka:9092"]
  topics: ["jobs.transcription.input"]
  consumerGroup: "inference-transcription"
  delivery:
    timeout: "PT12M"       # > timeoutSeconds du Service (600s)
    retry: 3               # pour les 500 (erreurs infra transitoires)
    backoffPolicy: exponential
    backoffDelay: "PT30S"
  sink:
    ref:
      apiVersion: serving.knative.dev/v1
      kind: Service
      name: kevent-dispatcher-transcription
```
