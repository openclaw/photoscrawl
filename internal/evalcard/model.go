package evalcard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"
)

type ollamaGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Images  []string       `json:"images"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

type ollamaGenerateResponse struct {
	Model              string `json:"model,omitempty"`
	CreatedAt          string `json:"created_at,omitempty"`
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	Error              string `json:"error,omitempty"`
	TotalDuration      int64  `json:"total_duration,omitempty"`
	LoadDuration       int64  `json:"load_duration,omitempty"`
	PromptEvalCount    int64  `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64  `json:"prompt_eval_duration,omitempty"`
	EvalCount          int64  `json:"eval_count,omitempty"`
	EvalDuration       int64  `json:"eval_duration,omitempty"`
}

type storedModelOutput struct {
	EvalID          string         `json:"eval_id"`
	Provider        string         `json:"provider"`
	Model           string         `json:"model"`
	PromptVersion   string         `json:"prompt_version"`
	ImagePath       string         `json:"image_path"`
	MetadataPath    string         `json:"metadata_path"`
	StartedAt       string         `json:"started_at"`
	CompletedAt     string         `json:"completed_at"`
	DurationMillis  int64          `json:"duration_millis"`
	Response        string         `json:"response,omitempty"`
	Error           string         `json:"error,omitempty"`
	OllamaTelemetry map[string]any `json:"ollama_telemetry,omitempty"`
}

func runModelCalls(ctx context.Context, outputDir, promptText string, inputs []preparedInput, models []string, generateURL, apiKey string, concurrency int) (int, int, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	type job struct {
		input preparedInput
		model string
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded, failed := 0, 0
	client := &http.Client{Timeout: 20 * time.Minute}
	for range concurrency {
		wg.Go(func() {
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				ok, err := runOneModelCall(ctx, client, outputDir, promptText, job.input, job.model, generateURL, apiKey)
				if err != nil {
					cancel(err)
					return
				}
				mu.Lock()
				if ok {
					succeeded++
				} else {
					failed++
				}
				mu.Unlock()
			}
		})
	}
dispatch:
	for _, input := range inputs {
		for _, model := range models {
			select {
			case <-ctx.Done():
				break dispatch
			case jobs <- job{input: input, model: model}:
			}
		}
	}
	close(jobs)
	wg.Wait()
	return succeeded, failed, context.Cause(ctx)
}

// Provider failures are saved as evidence; persistence failures abort the run.
func runOneModelCall(ctx context.Context, client *http.Client, outputDir, promptText string, input preparedInput, model, generateURL, apiKey string) (bool, error) {
	started := time.Now().UTC()
	out := storedModelOutput{
		EvalID: input.ID, Provider: "ollama", Model: model, PromptVersion: PromptVersion,
		ImagePath: input.ImagePath, MetadataPath: input.MetadataPath,
		StartedAt: started.Format(time.RFC3339Nano),
	}
	generated, callErr := generateCard(ctx, client, promptText, input, model, generateURL, apiKey)
	if callErr != nil {
		out.Error = callErr.Error()
	} else {
		out.Response = strings.TrimSpace(generated.Response)
		out.OllamaTelemetry = map[string]any{
			"total_duration":       generated.TotalDuration,
			"load_duration":        generated.LoadDuration,
			"prompt_eval_count":    generated.PromptEvalCount,
			"prompt_eval_duration": generated.PromptEvalDuration,
			"eval_count":           generated.EvalCount,
			"eval_duration":        generated.EvalDuration,
		}
	}
	out.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	out.DurationMillis = time.Since(started).Milliseconds()
	if err := writeModelOutput(outputDir, out); err != nil {
		return false, fmt.Errorf("write model output: %w", err)
	}
	return callErr == nil, nil
}

func generateCard(ctx context.Context, client *http.Client, promptText string, input preparedInput, model, generateURL, apiKey string) (ollamaGenerateResponse, error) {
	imageBytes, err := os.ReadFile(input.ImagePath)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	renderedPrompt, err := promptWithMetadata(promptText, input.MetadataJSON)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	requestBody, err := json.Marshal(ollamaGenerateRequest{
		Model: model, Prompt: renderedPrompt,
		Images: []string{base64.StdEncoding.EncodeToString(imageBytes)}, Stream: false,
		Options: map[string]any{"temperature": 0.1},
	})
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, generateURL, bytes.NewReader(requestBody))
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ollamaGenerateResponse{}, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var generated ollamaGenerateResponse
	if err := json.Unmarshal(body, &generated); err != nil {
		return ollamaGenerateResponse{}, err
	}
	if strings.TrimSpace(generated.Error) != "" {
		return ollamaGenerateResponse{}, errors.New(generated.Error)
	}
	return generated, nil
}

func writeModelOutput(outputDir string, out storedModelOutput) error {
	path := filepath.Join(outputDir, "raw", out.EvalID+"__ollama__"+safeName(out.Model)+"__"+PromptVersion+".json")
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func promptWithMetadata(promptText string, metadataJSON []byte) (string, error) {
	tmpl, err := template.New("photo-card").Option("missingkey=error").Parse(promptText)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]string{"MetadataJSON": string(metadataJSON)}); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
