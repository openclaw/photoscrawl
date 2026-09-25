package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func writeLocalModelClassification(ctx context.Context, tx *sql.Tx, input classifyInput, classifier localModelClassifier, result localModelResult, classifiedAt time.Time) (int, error) {
	if err := clearLocalModelObservations(ctx, tx, input.AssetID, classifier.modelID); err != nil {
		return 0, err
	}
	imagePath, _ := input.contentImagePath()
	evidenceID := stableID("evidence", input.AssetID, "content_classification", localModelClassifierSource, classifier.modelID, classifier.promptVersion)
	evidenceJSON, err := jsonText(map[string]any{
		"classifier":              localModelClassifierSource,
		"model_id":                classifier.modelID,
		"model_api":               classifier.api,
		"prompt_version":          classifier.promptVersion,
		"endpoint":                result.Endpoint,
		"network_scope":           "loopback",
		"image_transmitted":       true,
		"transmitted_image_bytes": result.ImageBytes,
		"image_sha256":            result.ImageSHA256,
		"image_extension":         strings.ToLower(filepath.Ext(imagePath)),
		"image_path_class":        input.localPathClass(imagePath),
		"classified_at":           classifiedAt.Format(time.RFC3339Nano),
		"raw_response":            result.RawResponse,
		"parsed_response":         result.Payload,
		"local_only":              true,
		"cloud_transmitted":       false,
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
		if err := insertObservationFTS(ctx, tx, observationID, input.AssetID, observation.ValueText, strings.Join(nonEmpty(observation.ObservationType, observation.ValueText, localModelClassifierSource, classifier.modelID), " ")); err != nil {
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

func writeModelRun(ctx context.Context, tx *sql.Tx, runID string, classifier localModelClassifier, endpoints map[string]struct{}, inputCount, httpRequestAttempts, httpResponses, contentClassified, failures int, completedAt time.Time) error {
	responseEndpoints := sortedEndpointSet(endpoints)
	metadataJSON, err := jsonText(map[string]any{
		"content_classified":         contentClassified,
		"failures":                   failures,
		"model_api":                  classifier.api,
		"requested_endpoint":         classifier.endpointURL,
		"response_endpoints":         responseEndpoints,
		"network_scope":              "loopback",
		"transmits_image_bytes":      true,
		"http_request_attempts":      httpRequestAttempts,
		"http_responses_received":    httpResponses,
		"successful_classifications": contentClassified,
		"cloud_transmitted":          false,
		"local_only":                 true,
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
where rowid in (
    select mapped.fts_rowid
    from observation_fts_rowid mapped
    join model_observation model on model.id = mapped.observation_id
    where model.asset_id = ? and model.source = ? and model.model_id = ?
  )
`, assetID, localModelClassifierSource, modelID); err != nil {
		return fmt.Errorf("clear model observation fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
delete from observation_fts_rowid
where observation_id in (
  select id from model_observation
  where asset_id = ? and source = ? and model_id = ?
)
`, assetID, localModelClassifierSource, modelID); err != nil {
		return fmt.Errorf("clear model observation FTS mappings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
delete from observation_term
where observation_id in (
    select id from model_observation
    where asset_id = ? and source = ? and model_id = ?
  )
`, assetID, localModelClassifierSource, modelID); err != nil {
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
