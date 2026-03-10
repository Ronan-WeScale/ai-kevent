// Package service holds the inference service registry.
// Adding a new inference type (e.g. "translation") requires only a new entry
// in config.yaml — no Go code modification.
package service

import (
	"fmt"
	"path/filepath"
	"strings"

	"kevent/gateway/internal/config"
)

// Def describes a registered inference service type.
type Def struct {
	Type          string
	InputTopic    string
	ResultTopic   string
	AcceptedExts  map[string]struct{} // empty = accept any extension
	MaxFileSizeMB int64

	// Sync / OpenAI-compatible mode (optional).
	InferenceURL string // full URL of the backend endpoint
	Model        string // value of the "model" field used for routing
	OpenAIPath   string // e.g. "/v1/audio/transcriptions"
}

// Registry maps service type names to their runtime definitions.
type Registry struct {
	defs   map[string]*Def
	bySync map[string]map[string]*Def // openai_path → model → Def
}

func NewRegistry(cfgs []config.ServiceConfig) *Registry {
	r := &Registry{
		defs:   make(map[string]*Def, len(cfgs)),
		bySync: make(map[string]map[string]*Def),
	}
	for _, cfg := range cfgs {
		exts := make(map[string]struct{}, len(cfg.AcceptedExts))
		for _, ext := range cfg.AcceptedExts {
			exts[strings.ToLower(ext)] = struct{}{}
		}
		def := &Def{
			Type:          cfg.Type,
			InputTopic:    cfg.InputTopic,
			ResultTopic:   cfg.ResultTopic,
			AcceptedExts:  exts,
			MaxFileSizeMB: cfg.MaxFileSizeMB,
			InferenceURL:  cfg.InferenceURL,
			Model:         cfg.Model,
			OpenAIPath:    cfg.OpenAIPath,
		}
		r.defs[cfg.Type] = def

		// Build the sync routing index.
		if cfg.OpenAIPath != "" && cfg.Model != "" && cfg.InferenceURL != "" {
			if r.bySync[cfg.OpenAIPath] == nil {
				r.bySync[cfg.OpenAIPath] = make(map[string]*Def)
			}
			r.bySync[cfg.OpenAIPath][cfg.Model] = def
		}
	}
	return r
}

// Get returns the definition for the given service type, or an error if unknown.
func (r *Registry) Get(serviceType string) (*Def, error) {
	d, ok := r.defs[serviceType]
	if !ok {
		return nil, fmt.Errorf("unknown service type %q", serviceType)
	}
	return d, nil
}

// RouteSync returns the Def whose OpenAIPath and Model match the request,
// enabling model-field-based routing for OpenAI-compatible endpoints.
func (r *Registry) RouteSync(openaiPath, model string) (*Def, error) {
	models, ok := r.bySync[openaiPath]
	if !ok {
		return nil, fmt.Errorf("no sync service configured for path %q", openaiPath)
	}
	if d, ok := models[model]; ok {
		return d, nil
	}
	available := make([]string, 0, len(models))
	for m := range models {
		available = append(available, m)
	}
	return nil, fmt.Errorf("no service configured for path %q with model %q (available: %v)", openaiPath, model, available)
}

// HasSyncServices reports whether at least one service has sync mode configured.
func (r *Registry) HasSyncServices() bool {
	return len(r.bySync) > 0
}

// SyncPaths returns the unique OpenAI paths that have a sync backend configured.
func (r *Registry) SyncPaths() []string {
	paths := make([]string, 0, len(r.bySync))
	for p := range r.bySync {
		paths = append(paths, p)
	}
	return paths
}

// ValidateFile checks that the file's extension is accepted by the service.
func (r *Registry) ValidateFile(serviceType, filename string) error {
	def, err := r.Get(serviceType)
	if err != nil {
		return err
	}
	if len(def.AcceptedExts) == 0 {
		return nil // no restriction configured
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if _, ok := def.AcceptedExts[ext]; !ok {
		accepted := make([]string, 0, len(def.AcceptedExts))
		for e := range def.AcceptedExts {
			accepted = append(accepted, e)
		}
		return fmt.Errorf("extension %q not accepted for service %q (accepted: %v)", ext, serviceType, accepted)
	}
	return nil
}

// Types returns all registered service type names.
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.defs))
	for t := range r.defs {
		types = append(types, t)
	}
	return types
}

// Models returns all service definitions that expose an OpenAI-compatible model
// (i.e. have a non-empty Model field). Used for the GET /v1/models endpoint.
func (r *Registry) Models() []*Def {
	defs := make([]*Def, 0, len(r.defs))
	for _, d := range r.defs {
		if d.Model != "" {
			defs = append(defs, d)
		}
	}
	return defs
}

// All returns all service definitions (used to start result consumers).
func (r *Registry) All() []*Def {
	defs := make([]*Def, 0, len(r.defs))
	for _, d := range r.defs {
		defs = append(defs, d)
	}
	return defs
}
