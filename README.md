# kevent-gateway

API Gateway pour les services d'inférence KServe. Deux modes de fonctionnement coexistent pour chaque service :

| Mode | Endpoints | Quand l'utiliser |
|---|---|---|
| **Async** (Kafka) | `POST /jobs`, `GET /jobs/{id}` | Fichiers lourds, traitements longs (>30s), besoin de webhook |
| **Sync** (proxy OpenAI) | `POST /v1/*` | Intégration avec les SDK OpenAI officiels, latence faible (<30s) |

## Architecture

### Mode async

```
Client
  │
  ▼
POST /jobs (multipart: type + file)
  │
  ├─ 1. Fichier → Scaleway Object Storage (S3)
  ├─ 2. Job record → Redis (status: pending)
  └─ 3. InputEvent → Kafka (topic propre au service)
                          │
                          ▼
                    KafkaSource → Knative → Dispatcher sidecar → modèle GPU
                                              │
                                              └─ ResultEvent → Kafka (result topic)
                                                                    │
                                              ┌─────────────────────┘
                                              │  (consumer interne)
                                              ▼
                                       Redis mis à jour (status: completed/failed)
                                       + Webhook POST si callback_url fourni
Client
  │
  ▼
GET /jobs/{id}  →  { status, result_url (presigned S3) }
```

### Mode sync (proxy OpenAI-compatible)

```
Client  (SDK OpenAI Python / JS)
  │
  ▼
POST /v1/audio/transcriptions   ← field "model" = "whisper-large-v3"
POST /v1/chat/completions        ← field "model" = "llava-v1.6-mistral-7b"
  │
  ▼  (routing par chemin + champ model)
Gateway SyncHandler
  │
  ▼
Dispatcher Knative  /v1/*  →  modèle GPU (127.0.0.1:9000)
  │
  ▼ (réponse streamée directement)
Client
```

### Composants externes requis

| Composant | Rôle |
|---|---|
| **Kafka** | Bus d'événements (mode async) |
| **Redis** | État des jobs (mode async, TTL configurable) |
| **Scaleway Object Storage** | Stockage fichiers d'entrée et résultats (mode async) |

---

## Démarrage rapide

### Prérequis

- Go 1.23+
- Kafka, Redis et un bucket Scaleway accessibles

### Build

```bash
go mod download
go build -o gateway ./cmd/gateway
./gateway
# ou avec un chemin de config custom :
CONFIG_PATH=/etc/kevent/config.yaml ./gateway
```

### Docker

```bash
docker build -t kevent-gateway .
docker run \
  -e S3_ACCESS_KEY=... \
  -e S3_SECRET_KEY=... \
  -e KAFKA_BROKERS=kafka:9092 \
  -e REDIS_ADDR=redis:6379 \
  -p 8080:8080 \
  kevent-gateway
```

---

## Configuration

La configuration est lue depuis `config.yaml` (chemin par défaut). Toutes les valeurs de la forme `${VAR:-défaut}` sont substituées depuis l'environnement au démarrage.

### Référence complète

```yaml
server:
  addr: ":8080"
  read_timeout: 120s    # élevé pour les gros uploads
  write_timeout: 0s     # 0 = désactivé — requis pour le mode sync (inférence longue)
  idle_timeout: 120s

kafka:
  brokers:
    - "kafka:9092"      # liste des brokers (multi-broker supporté)
  consumer_group: "kevent-gateway"

s3:
  endpoint: "https://s3.fr-par.scw.cloud"  # Scaleway : https://s3.<région>.scw.cloud
  region: "fr-par"                          # fr-par | nl-ams | pl-waw
  access_key: "<Scaleway Access Key ID>"
  secret_key: "<Scaleway Secret Key>"
  bucket: "kevent-jobs"
  presign_ttl_minutes: 60   # durée de validité des liens de téléchargement

redis:
  addr: "redis:6379"
  password: ""
  db: 0
  job_ttl_hours: 72    # durée de conservation des jobs (3 jours par défaut)

services:
  - type: transcription
    # ── Sync / OpenAI-compatible (optionnel) ──────────────────────────────
    # model        : valeur du champ "model" dans le payload, utilisée pour
    #                router POST /v1/audio/transcriptions vers ce service
    # openai_path  : chemin OpenAI exposé par le gateway
    # inference_url: URL complète du dispatcher Knative (inclut le chemin)
    model: "whisper-large-v3"
    openai_path: "/v1/audio/transcriptions"
    inference_url: "http://kevent-dispatcher-transcription.default.svc.cluster.local/v1/audio/transcriptions"
    # ── Async / Kafka ─────────────────────────────────────────────────────
    input_topic: jobs.transcription.input
    result_topic: jobs.transcription.results
    accepted_exts: [".mp3", ".wav", ".m4a", ".ogg", ".flac"]
    max_file_size_mb: 500

  - type: ocr
    model: "llava-v1.6-mistral-7b"
    openai_path: "/v1/chat/completions"
    inference_url: "http://kevent-dispatcher-ocr.default.svc.cluster.local/v1/chat/completions"
    input_topic: jobs.ocr.input
    result_topic: jobs.ocr.results
    accepted_exts: [".pdf", ".jpg", ".jpeg", ".png", ".tiff", ".bmp"]
    max_file_size_mb: 50
```

### Variables d'environnement

| Variable | Valeur par défaut | Description |
|---|---|---|
| `CONFIG_PATH` | `config.yaml` | Chemin vers le fichier de configuration |
| `S3_ENDPOINT` | `https://s3.fr-par.scw.cloud` | Endpoint S3 |
| `S3_REGION` | `fr-par` | Région Scaleway |
| `S3_ACCESS_KEY` | — | Access Key ID (**requis**) |
| `S3_SECRET_KEY` | — | Secret Key (**requis**) |
| `S3_BUCKET` | `kevent-jobs` | Nom du bucket |
| `KAFKA_BROKERS` | `kafka:9092` | Adresse(s) des brokers Kafka |
| `REDIS_ADDR` | `redis:6379` | Adresse Redis |
| `REDIS_PASSWORD` | _(vide)_ | Mot de passe Redis |
| `TRANSCRIPTION_INFERENCE_URL` | _(URL cluster locale)_ | URL du dispatcher transcription (mode sync) |
| `OCR_INFERENCE_URL` | _(URL cluster locale)_ | URL du dispatcher OCR (mode sync) |

> Les variables d'environnement surchargent les valeurs du fichier YAML via la syntaxe `${VAR:-défaut}`.

---

## Ajouter un service d'inférence

Aucun changement de code n'est nécessaire. Il suffit d'ajouter un bloc dans `config.yaml` et de redémarrer le gateway :

```yaml
services:
  # ... services existants ...

  - type: translation
    # Sync (si le dispatcher est déployé)
    model: "nllb-200"
    openai_path: "/v1/chat/completions"
    inference_url: "http://kevent-dispatcher-translation.default.svc.cluster.local/v1/chat/completions"
    # Async
    input_topic: jobs.translation.input
    result_topic: jobs.translation.results
    accepted_exts: [".txt", ".pdf", ".docx"]
    max_file_size_mb: 10
```

Le gateway démarrera automatiquement un consumer Kafka sur `result_topic` et acceptera les soumissions pour ce nouveau type.

> **Routing sync multi-modèles** : plusieurs services peuvent partager le même `openai_path` (ex: `/v1/chat/completions`) — le gateway sélectionne le backend d'après la valeur du champ `model` dans le payload.

> **Pré-requis Kafka** : les topics `input_topic` et `result_topic` doivent être créés avant le démarrage (`AllowAutoTopicCreation: false`).

---

## API

### Mode sync — Endpoints OpenAI-compatibles

Ces endpoints sont exposés si `model`, `openai_path` et `inference_url` sont configurés pour au moins un service.

Le gateway sélectionne le backend en lisant le champ `model` du payload, puis proxie la requête vers le dispatcher Knative correspondant. La réponse est streamée directement au client — aucun état Redis, aucun S3.

#### `POST /v1/audio/transcriptions` — Transcription audio

Compatible avec le SDK OpenAI Python/JS (même payload que `openai.audio.transcriptions.create`).

**Content-Type** : `multipart/form-data`

| Champ | Type | Requis | Description |
|---|---|---|---|
| `model` | string | oui | Identifiant du modèle : `whisper-large-v3` |
| `file` | file | oui | Fichier audio (.mp3, .wav, .m4a, .ogg, .flac) |
| `language` | string | non | Code langue ISO-639-1 (ex: `fr`, `en`) |
| `response_format` | string | non | `json` (défaut) \| `text` \| `verbose_json` |

**Exemple**

```bash
curl https://api.kevent.example.com/v1/audio/transcriptions \
  -F model=whisper-large-v3 \
  -F file=@interview.wav \
  -F language=fr
```

**Avec le SDK OpenAI Python**

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://api.kevent.example.com",
    api_key="unused",  # le gateway ne vérifie pas l'api_key (à sécuriser via Ingress)
)

with open("interview.wav", "rb") as f:
    transcript = client.audio.transcriptions.create(
        model="whisper-large-v3",
        file=f,
        language="fr",
    )
print(transcript.text)
```

---

#### `POST /v1/chat/completions` — Vision / OCR

Compatible avec le SDK OpenAI Python/JS (même payload que `openai.chat.completions.create`).

**Content-Type** : `application/json`

```json
{
  "model": "llava-v1.6-mistral-7b",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,<...>"}},
        {"type": "text", "text": "Extrait tout le texte visible dans ce document."}
      ]
    }
  ],
  "max_tokens": 4096
}
```

---

### Mode async — Jobs

#### `POST /jobs` — Soumettre un job

Soumet un fichier pour traitement asynchrone.

**Content-Type** : `multipart/form-data`

| Champ | Type | Requis | Description |
|---|---|---|---|
| `type` | string | oui | Type de service : `transcription`, `ocr`, … |
| `file` | file | oui | Fichier à traiter |
| `callback_url` | string | non | URL appelée en POST à la complétion du job |

**Réponse** `202 Accepted`

```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "service_type": "transcription",
  "status": "pending"
}
```

**Exemple**

```bash
curl -X POST http://localhost:8080/jobs \
  -F "type=transcription" \
  -F "file=@interview.wav" \
  -F "callback_url=https://mon-app.example.com/hooks/inference"
```

---

#### `GET /jobs/{id}` — Statut d'un job

Retourne l'état courant d'un job et, quand le traitement est terminé, un lien de téléchargement temporaire vers le résultat.

**Réponse** `200 OK`

```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "service_type": "transcription",
  "status": "completed",
  "result_url": "https://kevent-jobs.s3.fr-par.scw.cloud/550e8400-.../...",
  "created_at": "2026-03-05T10:00:00Z",
  "updated_at": "2026-03-05T10:04:32Z"
}
```

| Champ | Description |
|---|---|
| `status` | `pending` \| `processing` \| `completed` \| `failed` |
| `result_url` | URL présignée S3 valide `presign_ttl_minutes` minutes (présent uniquement si `completed`) |
| `error` | Message d'erreur (présent uniquement si `failed`) |

**Exemple — polling simple**

```bash
JOB_ID="550e8400-e29b-41d4-a716-446655440000"

while true; do
  RESPONSE=$(curl -s http://localhost:8080/jobs/$JOB_ID)
  STATUS=$(echo $RESPONSE | jq -r '.status')

  if [ "$STATUS" = "completed" ]; then
    RESULT_URL=$(echo $RESPONSE | jq -r '.result_url')
    curl -o result.json "$RESULT_URL"
    break
  elif [ "$STATUS" = "failed" ]; then
    echo "Erreur : $(echo $RESPONSE | jq -r '.error')"
    break
  fi

  sleep 10
done
```

---

### `GET /health`

```json
{ "status": "ok", "time": "2026-03-05T10:00:00Z" }
```

---

## Contrat Kafka (mode async)

### InputEvent — publié par le gateway sur `input_topic`

```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "service_type": "transcription",
  "input_ref": "550e8400-.../input.wav",
  "created_at": "2026-03-05T10:00:00Z"
}
```

Le champ `input_ref` est la clé objet S3 du fichier d'entrée. Le dispatcher doit le lire depuis le bucket configuré.

### ResultEvent — attendu par le gateway sur `result_topic`

Le dispatcher publie ce message quand le traitement est terminé (succès ou échec) :

```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "service_type": "transcription",
  "status": "completed",
  "result_ref": "550e8400-.../result.json",
  "error": "",
  "completed_at": "2026-03-05T10:04:32Z"
}
```

| Champ | Description |
|---|---|
| `status` | `completed` ou `failed` |
| `result_ref` | Clé objet S3 du fichier résultat (si `completed`) |
| `error` | Message d'erreur lisible (si `failed`) |

---

## Webhook (optionnel, mode async)

Si `callback_url` est fourni à la soumission, le gateway effectue un `POST` sur cette URL dès que le job passe à l'état `completed` ou `failed`. En cas d'échec HTTP (5xx ou timeout), 3 tentatives sont faites avec un backoff exponentiel (2 s → 4 s → 8 s).

**Corps de la requête**

```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "service_type": "transcription",
  "status": "completed",
  "result_ref": "550e8400-.../result.json",
  "completed_at": "2026-03-05T10:04:32Z"
}
```

---

## Structure du projet

```
.
├── cmd/gateway/main.go          # Point d'entrée — wiring et graceful shutdown
├── internal/
│   ├── config/config.go         # Chargement YAML + expansion des variables d'env
│   ├── model/job.go             # Types partagés : Job, InputEvent, ResultEvent
│   ├── service/registry.go      # Registre des services (config-driven, index sync)
│   ├── storage/
│   │   ├── s3.go                # Client S3 (AWS SDK v2) — configuré pour Scaleway
│   │   └── redis.go             # Persistance des jobs (JSON blob + TTL)
│   ├── kafka/
│   │   ├── producer.go          # Producteur Kafka — un writer par topic, lazy init
│   │   └── consumer.go          # Consumer de résultats — une goroutine par result_topic
│   └── handler/
│       ├── jobs.go              # POST /jobs  •  GET /jobs/{id}  (async)
│       ├── sync.go              # POST /v1/*  (proxy OpenAI-compatible, sync)
│       ├── health.go            # GET /health
│       └── middleware.go        # Logger structuré (slog/JSON)
├── config.yaml                  # Configuration par défaut
└── Dockerfile                   # Multi-stage build → image distroless (~10 MB)
```
