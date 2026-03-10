package handler

import (
	"encoding/json"
	"net/http"
	"time"
)

// Health handles GET /health.
func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"time":   time.Now().UTC(),
	})
}
