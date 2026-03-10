FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /dispatcher ./cmd/dispatcher

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /dispatcher /dispatcher
COPY config.yaml /config.yaml

ENTRYPOINT ["/dispatcher"]
