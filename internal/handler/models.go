package handler

import (
	"encoding/json"
	"net/http"
	"sort"

	"kevent/gateway/internal/service"
)

// modelObject mirrors the OpenAI model object returned by GET /v1/models.
type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type modelsListResponse struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

// ListModels handles GET /v1/models and returns all configured models
// in the OpenAI-compatible format.
func ListModels(registry *service.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defs := registry.Models()

		data := make([]modelObject, 0, len(defs))
		for _, d := range defs {
			data = append(data, modelObject{
				ID:      d.Model,
				Object:  "model",
				OwnedBy: "kevent",
			})
		}
		// Stable ordering regardless of map iteration.
		sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(modelsListResponse{Object: "list", Data: data})
	}
}
