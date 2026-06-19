package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	repoPrompts "github.com/openclaw/photoscrawl/prompts"
)

const (
	localModelClassifierSource = "local_multimodal"
	localModelPromptVersion    = repoPrompts.LocalMultimodalObservationsV1Version
	defaultOllamaGenerateURL   = "http://127.0.0.1:11434/api/generate"
)

type contentObservation struct {
	ObservationType string
	ValueText       string
	Value           any
	Confidence      float64
	TermType        string
}

type localModelResult struct {
	Payload      map[string]any
	RawResponse  string
	ImageBytes   int64
	ImageSHA256  string
	Observations []contentObservation
}

type localModelClassifier struct {
	modelID       string
	promptVersion string
	generateURL   string
	client        *http.Client
}

type ollamaGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Images  []string       `json:"images"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

func newLocalModelClassifier(modelID, generateURL string) localModelClassifier {
	generateURL = strings.TrimSpace(generateURL)
	if generateURL == "" {
		generateURL = defaultOllamaGenerateURL
	}
	return localModelClassifier{
		modelID:       strings.TrimSpace(modelID),
		promptVersion: localModelPromptVersion,
		generateURL:   generateURL,
		client:        &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c localModelClassifier) classify(ctx context.Context, imagePath string) (localModelResult, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return localModelResult{}, fmt.Errorf("read local image: %w", err)
	}
	sum := sha256.Sum256(data)
	requestBody, err := json.Marshal(ollamaGenerateRequest{
		Model:  c.modelID,
		Prompt: repoPrompts.LocalMultimodalObservationsV1,
		Images: []string{base64.StdEncoding.EncodeToString(data)},
		Stream: false,
		Options: map[string]any{
			"temperature": 0.1,
		},
	})
	if err != nil {
		return localModelResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.generateURL, bytes.NewReader(requestBody))
	if err != nil {
		return localModelResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return localModelResult{}, fmt.Errorf("call local model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return localModelResult{}, fmt.Errorf("local model returned %s", resp.Status)
	}
	var generated ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&generated); err != nil {
		return localModelResult{}, fmt.Errorf("decode local model response: %w", err)
	}
	if strings.TrimSpace(generated.Error) != "" {
		return localModelResult{}, errors.New(generated.Error)
	}
	payload, err := parseModelPayload(generated.Response)
	if err != nil {
		return localModelResult{}, err
	}
	return localModelResult{
		Payload:      payload,
		RawResponse:  strings.TrimSpace(generated.Response),
		ImageBytes:   int64(len(data)),
		ImageSHA256:  hex.EncodeToString(sum[:]),
		Observations: observationsFromPayload(payload),
	}, nil
}

func parseModelPayload(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("local model did not return a JSON object")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil {
		return nil, fmt.Errorf("parse local model JSON: %w", err)
	}
	return payload, nil
}

func observationsFromPayload(payload map[string]any) []contentObservation {
	out := []contentObservation{}
	add := func(kind string, value any, confidence float64, termType string) {
		for _, text := range valueTexts(value) {
			out = append(out, contentObservation{
				ObservationType: kind,
				ValueText:       text,
				Value:           map[string]any{"text": text},
				Confidence:      confidence,
				TermType:        termType,
			})
		}
	}
	add("scene_summary", payload["scene_summary"], 0.65, "scene")
	add("visible_text_summary", payload["visible_text_summary"], 0.6, "visible_text")
	add("place_type_candidate", payload["place_candidates"], 0.45, "place_type_candidate")
	add("landmark_or_place_name_candidate", payload["landmark_candidates"], 0.45, "landmark_or_place_name_candidate")
	add("merchant_or_venue_name_candidate", payload["merchant_or_venue_candidates"], 0.45, "merchant_or_venue_name_candidate")
	add("object_or_food", payload["food_or_objects"], 0.55, "object_or_food")
	add("people_presence", payload["people_presence"], 0.55, "people_presence")
	add("privacy_sensitivity", payload["privacy_sensitivity"], 0.6, "privacy_sensitivity")
	add("cluster_feature", payload["cluster_terms"], 0.55, "cluster_feature")
	add("model_uncertainty", payload["uncertainties"], 0.5, "model_uncertainty")
	for _, leakage := range promptLeakageObservations(payload) {
		out = append(out, leakage)
	}
	return dedupeContentObservations(out)
}

func promptLeakageObservations(payload map[string]any) []contentObservation {
	promptFragments := []string{
		"return only valid compact",
		"do not use markdown fences",
		"use candidates, not truth",
		"keys:",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(string(data))
	for _, fragment := range promptFragments {
		if strings.Contains(lower, fragment) {
			return []contentObservation{{
				ObservationType: "quality_issue",
				ValueText:       "model_prompt_leakage",
				Value:           map[string]any{"text": "model_prompt_leakage", "fragment": fragment},
				Confidence:      1,
				TermType:        "quality_issue",
			}}
		}
	}
	return nil
}

func valueTexts(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return nonEmpty(truncateObservationText(typed))
	case []any:
		out := []string{}
		for _, item := range typed {
			out = append(out, valueTexts(item)...)
		}
		return out
	case map[string]any:
		for _, key := range []string{"name", "label", "text", "value", "candidate"} {
			if text, ok := typed[key].(string); ok {
				return nonEmpty(truncateObservationText(text))
			}
		}
		data, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		return nonEmpty(truncateObservationText(string(data)))
	case bool:
		if typed {
			return []string{"true"}
		}
		return []string{"false"}
	case float64:
		return []string{fmt.Sprintf("%g", typed)}
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		return nonEmpty(truncateObservationText(text))
	}
}

func dedupeContentObservations(observations []contentObservation) []contentObservation {
	seen := map[string]bool{}
	out := make([]contentObservation, 0, len(observations))
	for _, observation := range observations {
		key := observation.ObservationType + "\x00" + observation.ValueText
		if seen[key] || strings.TrimSpace(observation.ValueText) == "" {
			continue
		}
		seen[key] = true
		out = append(out, observation)
	}
	return out
}

func writeLocalModelClassification(ctx context.Context, tx *sql.Tx, input classifyInput, classifier localModelClassifier, result localModelResult, classifiedAt time.Time) (int, error) {
	if err := clearLocalModelObservations(ctx, tx, input.AssetID, classifier.modelID); err != nil {
		return 0, err
	}
	imagePath, _ := input.contentImagePath()
	evidenceID := stableID("evidence", input.AssetID, "content_classification", localModelClassifierSource, classifier.modelID, classifier.promptVersion)
	evidenceJSON, err := jsonText(map[string]any{
		"classifier":        localModelClassifierSource,
		"model_id":          classifier.modelID,
		"prompt_version":    classifier.promptVersion,
		"image_bytes":       result.ImageBytes,
		"image_sha256":      result.ImageSHA256,
		"image_extension":   strings.ToLower(filepath.Ext(imagePath)),
		"image_path_class":  input.localPathClass(imagePath),
		"classified_at":     classifiedAt.Format(time.RFC3339Nano),
		"raw_response":      result.RawResponse,
		"parsed_response":   result.Payload,
		"local_only":        true,
		"cloud_transmitted": false,
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
insert into evidence_ref(id, asset_id, evidence_kind, source, pointer, value_json)
values (?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  asset_id = excluded.asset_id,
  evidence_kind = excluded.evidence_kind,
  source = excluded.source,
  pointer = excluded.pointer,
  value_json = excluded.value_json
`, evidenceID, input.AssetID, "content_classification", localModelClassifierSource, input.AssetID+"/classification/local_multimodal", evidenceJSON); err != nil {
		return 0, fmt.Errorf("write local model evidence: %w", err)
	}

	written := 0
	for _, observation := range result.Observations {
		valueJSON, err := jsonText(observation.Value)
		if err != nil {
			return written, err
		}
		observationID := stableID("model_observation", input.AssetID, localModelClassifierSource, classifier.modelID, classifier.promptVersion, observation.ObservationType, observation.ValueText)
		if _, err := tx.ExecContext(ctx, `
insert into model_observation(id, asset_id, observation_type, value_text, value_json, confidence, source, model_id, prompt_version, evidence_id)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, observationID, input.AssetID, observation.ObservationType, observation.ValueText, valueJSON, observation.Confidence, localModelClassifierSource, classifier.modelID, classifier.promptVersion, evidenceID); err != nil {
			return written, fmt.Errorf("write model observation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
insert into observation_fts(id, asset_id, title, body)
values (?, ?, ?, ?)
`, observationID, input.AssetID, observation.ValueText, strings.Join(nonEmpty(observation.ObservationType, observation.ValueText, localModelClassifierSource, classifier.modelID), " ")); err != nil {
			return written, fmt.Errorf("write model observation fts: %w", err)
		}
		for _, term := range observationTerms(observation) {
			termID := stableID("observation_term", input.AssetID, observationID, term)
			if _, err := tx.ExecContext(ctx, `
insert into observation_term(id, asset_id, observation_id, term, term_type, source, model_id)
values (?, ?, ?, ?, ?, ?, ?)
`, termID, input.AssetID, observationID, term, observation.TermType, localModelClassifierSource, classifier.modelID); err != nil {
				return written, fmt.Errorf("write observation term: %w", err)
			}
		}
		written++
	}
	if err := updateClassificationQueue(ctx, tx, input.QueueID, "content_classified", "local_model_observations", classifiedAt); err != nil {
		return written, err
	}
	return written, nil
}

func writeModelRun(ctx context.Context, tx *sql.Tx, runID string, classifier localModelClassifier, inputCount, contentClassified, failures int, completedAt time.Time) error {
	metadataJSON, err := jsonText(map[string]any{
		"content_classified": contentClassified,
		"failures":           failures,
		"local_only":         true,
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
insert into model_run(id, source, model_id, prompt_version, started_at, completed_at, input_count, metadata_json)
values (?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  completed_at = excluded.completed_at,
  input_count = excluded.input_count,
  metadata_json = excluded.metadata_json
`, runID, localModelClassifierSource, classifier.modelID, classifier.promptVersion, completedAt.Format(time.RFC3339Nano), completedAt.Format(time.RFC3339Nano), inputCount, metadataJSON); err != nil {
		return fmt.Errorf("write model run: %w", err)
	}
	return nil
}

func clearLocalModelObservations(ctx context.Context, tx *sql.Tx, assetID, modelID string) error {
	if strings.TrimSpace(assetID) == "" {
		return errors.New("asset id is required")
	}
	if _, err := tx.ExecContext(ctx, `
delete from observation_fts
where asset_id = ?
  and id in (
    select id from model_observation
    where asset_id = ? and source = ? and model_id = ?
  )
`, assetID, assetID, localModelClassifierSource, modelID); err != nil {
		return fmt.Errorf("clear model observation fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
delete from observation_term
where asset_id = ?
  and observation_id in (
    select id from model_observation
    where asset_id = ? and source = ? and model_id = ?
  )
`, assetID, assetID, localModelClassifierSource, modelID); err != nil {
		return fmt.Errorf("clear observation terms: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
delete from model_observation
where asset_id = ? and source = ? and model_id = ?
`, assetID, localModelClassifierSource, modelID); err != nil {
		return fmt.Errorf("clear model observations: %w", err)
	}
	return nil
}

func (input classifyInput) contentImagePath() (string, bool) {
	if input.MediaType != "image" {
		return "", false
	}
	for _, resource := range input.Resources {
		path := strings.TrimSpace(resource.LocalPath)
		if path == "" || !classifiableImagePath(path) {
			continue
		}
		return path, true
	}
	return "", false
}

func (input classifyInput) localPathClass(path string) string {
	for _, resource := range input.Resources {
		if resource.LocalPath != path {
			continue
		}
		value := strings.ToLower(strings.Join([]string{resource.ResourceType, resource.LocalPath}, " "))
		switch {
		case strings.Contains(value, "derivative"):
			return "derivative"
		case strings.Contains(value, "render"):
			return "render"
		case strings.Contains(value, "original"):
			return "original"
		default:
			return "local_media"
		}
	}
	return "unknown"
}

func observationTerms(observation contentObservation) []string {
	terms := []string{}
	for _, part := range strings.Fields(observation.ValueText) {
		if term := normalizeTerm(part); term != "" {
			terms = append(terms, term)
		}
	}
	if term := normalizeTerm(observation.ValueText); term != "" {
		terms = append(terms, term)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, term := range terms {
		if seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	return out
}

func normalizeTerm(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastUnderscore = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && builder.Len() > 0 {
				builder.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(builder.String(), "_")
	if len(out) < 2 || len(out) > 80 {
		return ""
	}
	return out
}

func truncateObservationText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 500 {
		return value
	}
	return strings.TrimSpace(value[:500])
}

func truncateReason(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 200 {
		return value
	}
	return strings.TrimSpace(value[:200])
}

func classifiableImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".heic":
		return true
	default:
		return false
	}
}
