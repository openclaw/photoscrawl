package archive

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type contentObservation struct {
	ObservationType string
	ValueText       string
	Value           any
	Confidence      float64
	TermType        string
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
		case strings.Contains(value, "classification_preview"):
			return "classification_preview"
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
	return truncateText(value, 500)
}

func truncateReason(value string) string {
	return truncateText(value, 200)
}

func truncateText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit])
}

func classifiableImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".heic":
		return true
	default:
		return false
	}
}
