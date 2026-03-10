package adapter

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"kevent/dispatcher/internal/config"
)

type transcriptionAdapter struct {
	cfg    config.TranscriptionConfig
	client *http.Client
}

func newTranscription(cfg config.TranscriptionConfig) *transcriptionAdapter {
	return &transcriptionAdapter{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.TimeoutDuration()},
	}
}

// Call streams the audio file to the transcription endpoint via multipart form.
// An io.Pipe avoids loading the file (up to 500 MB) into memory.
func (a *transcriptionAdapter) Call(ctx context.Context, input CallInput) ([]byte, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		err := func() error {
			if err := mw.WriteField("model", a.cfg.Model); err != nil {
				return err
			}
			if a.cfg.Language != "" {
				if err := mw.WriteField("language", a.cfg.Language); err != nil {
					return err
				}
			}
			if err := mw.WriteField("response_format", a.cfg.ResponseFormat); err != nil {
				return err
			}
			part, err := mw.CreateFormFile("file", input.Filename)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, input.Body); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err) // nil → io.EOF (normal close)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.EndpointURL, pr)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if a.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling transcription endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("transcription endpoint returned %d: %s", resp.StatusCode, body)
	}

	return body, nil
}
