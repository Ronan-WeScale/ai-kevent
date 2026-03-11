package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kevent/gateway/internal/service"
)

// SyncHandler proxies OpenAI-compatible requests directly to the matching
// inference backend, selected by the "model" field in the request payload.
//
// Routing table: registry.RouteSync(r.URL.Path, model) → InferenceURL
//
// Supported content types:
//   - multipart/form-data  — audio/document endpoints (model field in form)
//   - application/json     — chat/completion endpoints (model field in JSON)
type SyncHandler struct {
	registry   *service.Registry
	httpClient *http.Client
}

func NewSyncHandler(registry *service.Registry) *SyncHandler {
	return &SyncHandler{
		registry: registry,
		// Generous timeout: audio transcription can take several minutes.
		// The real ceiling is Knative spec.template.spec.timeoutSeconds.
		httpClient: &http.Client{Timeout: 15 * time.Minute},
	}
}

func (h *SyncHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))

	var (
		model       string
		forwardBody io.ReadCloser
		forwardCT   string
	)

	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		// Parse multipart; files > 32 MB are spooled to disk automatically.
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
			return
		}
		model = r.FormValue("model")

		body, contentType, err := reconstructMultipart(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to rebuild request: "+err.Error())
			return
		}
		forwardBody = body
		forwardCT = contentType

	case ct == "application/json":
		// Read the JSON body to extract the model field, then restore it for
		// forwarding. Cap at 1 MB — JSON payloads don't carry file data.
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
			return
		}
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &payload)
		model = payload.Model
		forwardBody = io.NopCloser(bytes.NewReader(raw))
		forwardCT = r.Header.Get("Content-Type")

	default:
		writeError(w, http.StatusUnsupportedMediaType,
			"Content-Type must be multipart/form-data or application/json")
		return
	}
	defer forwardBody.Close()

	if model == "" {
		writeError(w, http.StatusBadRequest, "field 'model' is required")
		return
	}

	def, err := h.registry.RouteSync(r.URL.Path, model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Build the upstream URL: base URL from config + original request path + query.
	target, err := url.Parse(def.InferenceURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid inference_url configuration")
		return
	}
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), forwardBody)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build upstream request")
		return
	}
	upstreamReq.Header.Set("Content-Type", forwardCT)
	if auth := r.Header.Get("Authorization"); auth != "" {
		upstreamReq.Header.Set("Authorization", auth)
	}

	resp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Stream response back to the client.
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// reconstructMultipart rebuilds the multipart body from the already-parsed
// form, streaming file parts from their on-disk spool files via an io.Pipe.
// This avoids loading large files into memory a second time.
func reconstructMultipart(r *http.Request) (io.ReadCloser, string, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		// Text fields first.
		for key, values := range r.MultipartForm.Value {
			for _, value := range values {
				if err := mw.WriteField(key, value); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
		}
		// File fields — streamed from temp files on disk.
		for fieldName, fileHeaders := range r.MultipartForm.File {
			for _, fh := range fileHeaders {
				part, err := mw.CreateFormFile(fieldName, fh.Filename)
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				f, err := fh.Open()
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				_, err = io.Copy(part, f)
				f.Close()
				if err != nil {
					pw.CloseWithError(err)
					return
				}
			}
		}
		pw.CloseWithError(mw.Close())
	}()

	return pr, mw.FormDataContentType(), nil
}
