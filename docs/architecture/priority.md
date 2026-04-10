# Priority routing

Priority routing lets SA (service account) consumers preempt normal async jobs, ensuring low-latency processing for premium workloads.

## How it works

### Gateway side

When a request carries the priority header (configurable via `server.priority_header`), the gateway publishes the `InputEvent` to the `priority_topic` instead of the normal `input_topic`.

```
Normal async:   Kafka jobs.<model>.input    → relay POST /
Priority async: Kafka jobs.<model>.priority → relay POST /sync
```

### Relay side

The relay's `POST /sync` handler sets `syncPriority++` for the duration of the job. Concurrent `POST /` (normal async) handlers check this counter:

- `syncPriority > 0` → return `503 Service Unavailable`
- KafkaSource retries after `backoffDelay`
- Once the priority job finishes, `syncPriority--` and async jobs resume

This works across pod scale-out: each pod independently tracks its own priority state. No shared state across pods is needed.

## Configuration

```yaml
server:
  priority_header: "X-Priority"   # header name to check on incoming requests

services:
  - type: audio
    model: "whisper-large-v3"
    priority_topic: jobs.whisper-large-v3.priority   # omit to disable priority routing
    ...
```

### Knative KafkaSource for priority

Add a KafkaSource routing the priority topic to `POST /sync` on the relay:

```yaml
apiVersion: sources.knative.dev/v1beta1
kind: KafkaSource
metadata:
  name: kafka-source-whisper-priority
spec:
  topics:
    - jobs.whisper-large-v3.priority
  consumerGroup: kevent-relay-priority
  sink:
    ref:
      apiVersion: serving.knative.dev/v1
      kind: Service
      name: kevent-transcription
  delivery:
    backoffDelay: PT2S
    backoffPolicy: exponential
  extensions:
    path: /sync
```

## Consumer identification

Requests can carry a consumer name via a configurable header (`server.consumer_header`). The gateway:

1. Attaches the consumer name to the job record in Redis
2. Maintains a sorted set `consumer:{name}:jobs` for `GET /jobs` listing
3. Increments `kevent_jobs_by_consumer_total{service_type, model, consumer}`

```yaml
server:
  consumer_header: "X-Consumer-Username"   # set by APISIX after authentication
```
