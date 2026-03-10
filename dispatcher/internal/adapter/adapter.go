package adapter

import (
	"context"
	"fmt"
	"io"

	"kevent/dispatcher/internal/config"
)

// CallInput contains the data passed to an adapter for inference.
type CallInput struct {
	JobID       string
	Filename    string
	ContentType string
	Size        int64     // -1 if unknown
	Body        io.Reader // stream from S3; caller closes
}

// Adapter sends an inference request to a KServe-compatible endpoint
// and returns the raw JSON response body.
type Adapter interface {
	Call(ctx context.Context, input CallInput) ([]byte, error)
}

// New returns the Adapter implementation matching cfg.Service.Type.
func New(cfg *config.Config) (Adapter, error) {
	switch cfg.Service.Type {
	case "transcription":
		return newTranscription(cfg.Transcription), nil
	case "diarization":
		return newDiarization(cfg.Diarization), nil
	case "ocr":
		return newOCR(cfg.OCR), nil
	default:
		return nil, fmt.Errorf("unknown service type: %q", cfg.Service.Type)
	}
}
