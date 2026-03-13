package service_test

import (
	"testing"

	"kevent/gateway/internal/config"
	"kevent/gateway/internal/service"
)

func baseServiceConfig() config.ServiceConfig {
	return config.ServiceConfig{
		Type:          "transcription",
		Model:         "whisper-large-v3",
		OpenAIPaths:   []string{"/v1/audio/transcriptions", "/v1/audio/translations"},
		InferenceURL:  "http://inference.svc.cluster.local",
		InputTopic:    "jobs.whisper-large-v3.input",
		ResultTopic:   "jobs.whisper-large-v3.results",
		SyncTopic:     "jobs.whisper-large-v3.sync",
		AcceptedExts:  []string{".mp3", ".wav"},
		MaxFileSizeMB: 100,
	}
}

// TestDef_SyncTopicPopulated verifies that SyncTopic from config is exposed in the Def.
func TestDef_SyncTopicPopulated(t *testing.T) {
	reg := service.NewRegistry([]config.ServiceConfig{baseServiceConfig()})

	def, err := reg.RouteSync("/v1/audio/transcriptions", "whisper-large-v3")
	if err != nil {
		t.Fatalf("RouteSync failed: %v", err)
	}

	if def.SyncTopic != "jobs.whisper-large-v3.sync" {
		t.Errorf("expected SyncTopic %q, got %q", "jobs.whisper-large-v3.sync", def.SyncTopic)
	}
}

// TestRegistry_IndexedWithoutInferenceURL verifies that a service configured with
// only a sync_topic (no inference_url) is still indexed for sync routing.
func TestRegistry_IndexedWithoutInferenceURL(t *testing.T) {
	cfg := baseServiceConfig()
	cfg.InferenceURL = "" // no direct proxy — only Kafka sync path

	reg := service.NewRegistry([]config.ServiceConfig{cfg})

	def, err := reg.RouteSync("/v1/audio/transcriptions", "whisper-large-v3")
	if err != nil {
		t.Fatalf("service with only SyncTopic should be routable: %v", err)
	}
	if def.SyncTopic == "" {
		t.Error("SyncTopic should be set")
	}
}

// TestRegistry_NotIndexedWithoutModelOrTopic verifies that a service without a
// model is not indexed for sync routing.
func TestRegistry_NotIndexedWithoutModel(t *testing.T) {
	cfg := baseServiceConfig()
	cfg.Model = ""
	cfg.OpenAIPaths = []string{"/v1/audio/transcriptions"}

	reg := service.NewRegistry([]config.ServiceConfig{cfg})

	if reg.HasSyncServices() {
		t.Error("service without model should not be indexed for sync")
	}
}

// TestRouteSync_UnknownPathReturnsError verifies that an unknown path returns an error.
func TestRouteSync_UnknownPathReturnsError(t *testing.T) {
	reg := service.NewRegistry([]config.ServiceConfig{baseServiceConfig()})

	_, err := reg.RouteSync("/v1/unknown", "whisper-large-v3")
	if err == nil {
		t.Error("expected error for unknown path")
	}
}

// TestRouteSync_UnknownModelReturnsError verifies that an unknown model returns an error.
func TestRouteSync_UnknownModelReturnsError(t *testing.T) {
	reg := service.NewRegistry([]config.ServiceConfig{baseServiceConfig()})

	_, err := reg.RouteSync("/v1/audio/transcriptions", "unknown-model")
	if err == nil {
		t.Error("expected error for unknown model")
	}
}

// TestRegistry_MultipleServices verifies routing across two service types.
func TestRegistry_MultipleServices(t *testing.T) {
	cfgs := []config.ServiceConfig{
		baseServiceConfig(),
		{
			Type:         "ocr",
			Model:        "llava-v1.6-mistral-7b",
			OpenAIPaths:  []string{"/v1/chat/completions"},
			InferenceURL: "http://ocr.svc.cluster.local",
			InputTopic:   "jobs.llava.input",
			ResultTopic:  "jobs.llava.results",
			// No SyncTopic — JSON-based, uses direct proxy
		},
	}
	reg := service.NewRegistry(cfgs)

	def, err := reg.RouteSync("/v1/chat/completions", "llava-v1.6-mistral-7b")
	if err != nil {
		t.Fatalf("RouteSync failed: %v", err)
	}
	if def.SyncTopic != "" {
		t.Errorf("OCR should have empty SyncTopic, got %q", def.SyncTopic)
	}

	def2, err := reg.RouteSync("/v1/audio/transcriptions", "whisper-large-v3")
	if err != nil {
		t.Fatalf("RouteSync failed: %v", err)
	}
	if def2.SyncTopic == "" {
		t.Error("transcription should have non-empty SyncTopic")
	}
}

// TestRouteAsync_SyncTopicPreserved verifies that RouteAsync also exposes SyncTopic.
func TestRouteAsync_SyncTopicPreserved(t *testing.T) {
	reg := service.NewRegistry([]config.ServiceConfig{baseServiceConfig()})

	def, err := reg.RouteAsync("transcription", "whisper-large-v3")
	if err != nil {
		t.Fatalf("RouteAsync failed: %v", err)
	}
	if def.SyncTopic != "jobs.whisper-large-v3.sync" {
		t.Errorf("expected SyncTopic in RouteAsync result, got %q", def.SyncTopic)
	}
}
