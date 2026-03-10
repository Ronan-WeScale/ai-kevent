package dispatcher

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// SyncProxy handles direct OpenAI-compatible requests (POST /v1/*) by
// forwarding them transparently to the inference model running at inferenceBase.
//
// This is the sync path: gateway → dispatcher /v1/* → model (127.0.0.1:9000).
// No S3, no Kafka — the response is streamed directly back to the caller.
type SyncProxy struct {
	inferenceBase string // e.g. "http://127.0.0.1:9000"
	client        *http.Client
}

// NewSyncProxy creates a proxy that forwards requests to inferenceBase + request path.
// inferenceBase must not end with a slash (e.g. "http://127.0.0.1:9000").
func NewSyncProxy(inferenceBase string) *SyncProxy {
	return &SyncProxy{
		inferenceBase: strings.TrimRight(inferenceBase, "/"),
		// Timeout is managed by Knative spec.template.spec.timeoutSeconds.
		client: &http.Client{Timeout: 0},
	}
}

// ServeHTTP forwards the request to the inference model and streams the response back.
func (p *SyncProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	targetURL := p.inferenceBase + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		slog.Error("sync proxy: failed to build request", "target", targetURL, "error", err)
		http.Error(w, "proxy error", http.StatusInternalServerError)
		return
	}
	proxyReq.Header = r.Header.Clone()
	proxyReq.Header.Del("Connection") // hop-by-hop header

	resp, err := p.client.Do(proxyReq)
	if err != nil {
		slog.Error("sync proxy: upstream error", "target", targetURL, "error", err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
