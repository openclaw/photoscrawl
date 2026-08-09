package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
)

const preciseLocationAccuracyMeters = 100

type TimelineOptions struct {
	From string
	To   string
}

type TimelineResult struct {
	From         string                `json:"from"`
	To           string                `json:"to"`
	Count        int                   `json:"count"`
	Observations []TimelineObservation `json:"observations"`
}

type TimelineObservation struct {
	AssetID               string   `json:"asset_id"`
	LocationObservationID string   `json:"location_observation_id"`
	SourceRef             string   `json:"source_ref"`
	CreatedAt             string   `json:"created_at"`
	MediaType             string   `json:"media_type"`
	Latitude              float64  `json:"lat"`
	Longitude             float64  `json:"lng"`
	AccuracyMeters        *float64 `json:"accuracy_m,omitempty"`
	IsPrecise             *bool    `json:"is_precise,omitempty"`
}

func Timeline(ctx context.Context, paths Paths, opts TimelineOptions) (TimelineResult, error) {
	from, err := timelineBound("from", opts.From)
	if err != nil {
		return TimelineResult{}, err
	}
	to, err := timelineBound("to", opts.To)
	if err != nil {
		return TimelineResult{}, err
	}
	if !from.Before(to) {
		return TimelineResult{}, errors.New("from must be before to")
	}

	fromText := from.UTC().Format(time.RFC3339Nano)
	toText := to.UTC().Format(time.RFC3339Nano)
	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		return TimelineResult{}, err
	}
	defer db.Close()

	rows, err := db.DB().QueryContext(ctx, `
select asset.id, location_observation.id, asset.creation_date, asset.media_type,
       location_observation.latitude, location_observation.longitude,
       location_observation.horizontal_accuracy
from asset
join location_observation on location_observation.asset_id = asset.id
where julianday(asset.creation_date) >= julianday(?)
  and julianday(asset.creation_date) < julianday(?)
  and not exists (
    select 1
    from classification_queue
    where classification_queue.asset_id = asset.id
      and classification_queue.state = 'deleted'
  )
order by julianday(asset.creation_date), asset.creation_date, asset.id, location_observation.id
`, fromText, toText)
	if err != nil {
		return TimelineResult{}, fmt.Errorf("query photo timeline: %w", err)
	}
	defer rows.Close()

	result := TimelineResult{From: fromText, To: toText, Observations: []TimelineObservation{}}
	for rows.Next() {
		var observation TimelineObservation
		var accuracy sql.NullFloat64
		if err := rows.Scan(
			&observation.AssetID,
			&observation.LocationObservationID,
			&observation.CreatedAt,
			&observation.MediaType,
			&observation.Latitude,
			&observation.Longitude,
			&accuracy,
		); err != nil {
			return TimelineResult{}, err
		}
		observation.SourceRef = observation.LocationObservationID
		if accuracy.Valid {
			accuracyMeters := accuracy.Float64
			isPrecise := accuracyMeters >= 0 && accuracyMeters <= preciseLocationAccuracyMeters
			observation.AccuracyMeters = &accuracyMeters
			observation.IsPrecise = &isPrecise
		}
		result.Observations = append(result.Observations, observation)
	}
	if err := rows.Err(); err != nil {
		return TimelineResult{}, err
	}
	result.Count = len(result.Observations)
	return result, nil
}

func timelineBound(name, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an ISO 8601 timestamp with an offset: %w", name, err)
	}
	return parsed, nil
}
