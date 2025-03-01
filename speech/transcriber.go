package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Transcriber handles audio transcription
type Transcriber struct {
	APIKey  string
	Timeout time.Duration
}

// NewTranscriber creates a new Transcriber
func NewTranscriber(apiKey string, timeout time.Duration) *Transcriber {
	return &Transcriber{
		APIKey:  apiKey,
		Timeout: timeout,
	}
}

// TranscribeFile transcribes an audio file
func (t *Transcriber) TranscribeFile(ctx context.Context, filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create multipart form
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	// Add file to form
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err = io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file to form: %w", err)
	}

	// Add model to form
	if err = writer.WriteField("model", "whisper-1"); err != nil {
		return "", fmt.Errorf("failed to write model field: %w", err)
	}

	// Close writer
	if err = writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close writer: %w", err)
	}

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/transcriptions", &requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+t.APIKey)

	// Make request
	client := &http.Client{Timeout: t.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status: %d, body: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var whisperResp struct {
		Text string `json:"text"`
	}

	if err = json.Unmarshal(respBody, &whisperResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	return whisperResp.Text, nil
}
